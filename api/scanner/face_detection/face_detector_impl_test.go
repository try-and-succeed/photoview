//go:build !no_face_detection

package face_detection

import (
	"testing"

	"gorm.io/gorm"

	"github.com/photoview/photoview/api/graphql/models"
	"github.com/photoview/photoview/api/test_utils"
)

func TestMain(m *testing.M) {
	test_utils.IntegrationTestRun(m)
}

// descriptorAt returns a descriptor whose components all equal value. Two of
// them lie a squared distance of 128*(a-b)^2 apart, which is what go-face
// compares with the tolerance of 0.2.
func descriptorAt(value float32) models.FaceDescriptor {
	var d models.FaceDescriptor
	for i := range d {
		d[i] = value
	}
	return d
}

const (
	base = 0.05
	// 128 * 0.0342^2 = 0.15: within the tolerance of base, and two steps
	// (0.6) beyond it.
	step = 0.0342
)

type detectorFixture struct {
	db    *gorm.DB
	user  *models.User
	media []models.Media
}

func setupDetectorTest(t *testing.T) *detectorFixture {
	t.Helper()

	db := test_utils.DatabaseTest(t)

	previous := GlobalFaceDetector
	t.Cleanup(func() { GlobalFaceDetector = previous })

	pass := "1234"
	user, err := models.RegisterUser(db, "detector_user", &pass, false)
	if err != nil {
		t.Fatal("register user error:", err)
	}

	album := models.Album{Title: "album", Path: "/detector-album"}
	if err := db.Create(&album).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(user).Association("Albums").Append(&album); err != nil {
		t.Fatal(err)
	}

	media := []models.Media{
		{Title: "one", Path: "/detector-album/one.jpg", AlbumID: album.ID},
		{Title: "two", Path: "/detector-album/two.jpg", AlbumID: album.ID},
		{Title: "three", Path: "/detector-album/three.jpg", AlbumID: album.ID},
	}
	if err := db.Create(&media).Error; err != nil {
		t.Fatal(err)
	}

	return &detectorFixture{db: db, user: user, media: media}
}

func (f *detectorFixture) group(t *testing.T, label *string) *models.FaceGroup {
	t.Helper()
	group := &models.FaceGroup{Label: label}
	if err := f.db.Create(group).Error; err != nil {
		t.Fatal(err)
	}
	return group
}

func (f *detectorFixture) face(t *testing.T, group *models.FaceGroup, mediaIndex int, descriptor models.FaceDescriptor) *models.ImageFace {
	t.Helper()
	face := &models.ImageFace{
		FaceGroupID: group.ID,
		MediaID:     f.media[mediaIndex].ID,
		Descriptor:  descriptor,
	}
	if err := f.db.Create(face).Error; err != nil {
		t.Fatal(err)
	}
	return face
}

func (f *detectorFixture) initialize(t *testing.T) *faceDetector {
	t.Helper()
	if err := InitializeFaceDetector(f.db); err != nil {
		t.Fatal("initialize face detector error:", err)
	}
	detector, ok := GlobalFaceDetector.(*faceDetector)
	if !ok {
		t.Fatalf("GlobalFaceDetector is %T, want *faceDetector", GlobalFaceDetector)
	}
	return detector
}

// primeRecognizer hands the detector's samples to the recognizer the way the
// scan of a new photo does, so a test starts from a recognizer that has seen
// every face.
func (fd *faceDetector) primeRecognizer() {
	fd.mutex.Lock()
	defer fd.mutex.Unlock()
	fd.rec.SetSamples(fd.faceDescriptors, fd.faceGroupIDs)
}

func (fd *faceDetector) classifyForTest(descriptor models.FaceDescriptor) int32 {
	fd.mutex.Lock()
	defer fd.mutex.Unlock()
	return fd.classifyDescriptor([128]float32(descriptor))
}

func TestFaceDetectorKeepsRecognizerInStep(t *testing.T) {
	t.Run("the faces in the database count right after start", func(t *testing.T) {
		f := setupDetectorTest(t)
		group := f.group(t, nil)
		f.face(t, group, 0, descriptorAt(base))

		detector := f.initialize(t)

		if got := detector.classifyForTest(descriptorAt(base)); got != int32(group.ID) {
			t.Errorf("classify after start = %d, want group %d", got, group.ID)
		}
	})

	t.Run("merged categories are classified into the destination", func(t *testing.T) {
		f := setupDetectorTest(t)
		source, dest := f.group(t, nil), f.group(t, nil)
		f.face(t, source, 0, descriptorAt(base))
		detector := f.initialize(t)

		detector.MergeCategories(int32(source.ID), int32(dest.ID))

		if got := detector.classifyForTest(descriptorAt(base)); got != int32(dest.ID) {
			t.Errorf("classify after merge = %d, want group %d", got, dest.ID)
		}
	})

	t.Run("moved faces are classified into their new group", func(t *testing.T) {
		f := setupDetectorTest(t)
		source, dest := f.group(t, nil), f.group(t, nil)
		face := f.face(t, source, 0, descriptorAt(base))
		detector := f.initialize(t)

		detector.MergeImageFaces([]int{face.ID}, int32(dest.ID))

		if got := detector.classifyForTest(descriptorAt(base)); got != int32(dest.ID) {
			t.Errorf("classify after move = %d, want group %d", got, dest.ID)
		}
	})

	t.Run("a reload forgets faces deleted from the database", func(t *testing.T) {
		f := setupDetectorTest(t)
		group := f.group(t, nil)
		face := f.face(t, group, 0, descriptorAt(base))
		detector := f.initialize(t)
		detector.primeRecognizer()

		if err := f.db.Delete(face).Error; err != nil {
			t.Fatal(err)
		}
		if err := detector.ReloadFacesFromDatabase(f.db); err != nil {
			t.Fatal("reload error:", err)
		}

		if got := detector.classifyForTest(descriptorAt(base)); got != -1 {
			t.Errorf("classify after reload = %d, want no match (-1)", got)
		}
	})
}

func TestRecognizeUnlabeledFacesWithoutLabeledFaces(t *testing.T) {
	f := setupDetectorTest(t)
	group := f.group(t, nil)
	face := f.face(t, group, 0, descriptorAt(base))
	detector := f.initialize(t)
	detector.primeRecognizer()

	// Nothing to match against: a face must not find itself.
	var updated []*models.ImageFace
	err := f.db.Transaction(func(tx *gorm.DB) error {
		var err error
		updated, err = detector.RecognizeUnlabeledFaces(tx, f.user)
		return err
	})
	if err != nil {
		t.Fatal("RecognizeUnlabeledFaces error:", err)
	}
	if len(updated) != 0 {
		t.Errorf("updated faces = %+v, want none", updated)
	}

	var stored models.ImageFace
	if err := f.db.First(&stored, face.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.FaceGroupID != group.ID {
		t.Errorf("face is in group %d, want it to stay in %d", stored.FaceGroupID, group.ID)
	}
}

func TestRecognizeUnlabeledFacesKeepsUnlabeledApart(t *testing.T) {
	f := setupDetectorTest(t)
	// Two similar faces, no labeled face: the first one must not become a
	// reference that pulls the second into its unlabeled group.
	firstGroup, secondGroup := f.group(t, nil), f.group(t, nil)
	f.face(t, firstGroup, 0, descriptorAt(base))
	second := f.face(t, secondGroup, 1, descriptorAt(base+step))
	detector := f.initialize(t)
	detector.primeRecognizer()

	var updated []*models.ImageFace
	err := f.db.Transaction(func(tx *gorm.DB) error {
		var err error
		updated, err = detector.RecognizeUnlabeledFaces(tx, f.user)
		return err
	})
	if err != nil {
		t.Fatal("RecognizeUnlabeledFaces error:", err)
	}
	if len(updated) != 0 {
		t.Errorf("updated faces = %+v, want none", updated)
	}

	var stored models.ImageFace
	if err := f.db.First(&stored, second.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.FaceGroupID != secondGroup.ID {
		t.Errorf("second face is in group %d, want it to stay in %d", stored.FaceGroupID, secondGroup.ID)
	}
}

func TestRecognizeUnlabeledFaces(t *testing.T) {
	f := setupDetectorTest(t)
	anna := "Anna"
	labeled := f.group(t, &anna)
	f.face(t, labeled, 0, descriptorAt(base))

	// Close to Anna: moves to her.
	nearGroup := f.group(t, nil)
	near := f.face(t, nearGroup, 1, descriptorAt(base+step))
	// Close only to the near face, not to Anna: references stay fixed for
	// the whole run, so it must not follow the near face to her.
	chainedGroup := f.group(t, nil)
	chained := f.face(t, chainedGroup, 2, descriptorAt(base+2*step))

	detector := f.initialize(t)
	detector.primeRecognizer()

	var updated []*models.ImageFace
	err := f.db.Transaction(func(tx *gorm.DB) error {
		var err error
		updated, err = detector.RecognizeUnlabeledFaces(tx, f.user)
		return err
	})
	if err != nil {
		t.Fatal("RecognizeUnlabeledFaces error:", err)
	}

	if len(updated) != 1 || updated[0].ID != near.ID || updated[0].FaceGroupID != labeled.ID {
		t.Fatalf("updated faces = %+v, want face %d in group %d", updated, near.ID, labeled.ID)
	}

	groupOf := func(face *models.ImageFace) int {
		var stored models.ImageFace
		if err := f.db.First(&stored, face.ID).Error; err != nil {
			t.Fatal(err)
		}
		return stored.FaceGroupID
	}
	if got := groupOf(near); got != labeled.ID {
		t.Errorf("near face is in group %d, want %d", got, labeled.ID)
	}
	if got := groupOf(chained); got != chainedGroup.ID {
		t.Errorf("chained face is in group %d, want it to stay in %d", got, chainedGroup.ID)
	}

	var count int64
	if err := f.db.Model(&models.FaceGroup{}).Where("id = ?", nearGroup.ID).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Errorf("the emptied unlabeled group %d should be deleted", nearGroup.ID)
	}

	// The recognizer sees the outcome too.
	if got := detector.classifyForTest(descriptorAt(base + 2*step)); got != int32(chainedGroup.ID) {
		t.Errorf("classify the chained face = %d, want its own group %d", got, chainedGroup.ID)
	}
	if got := detector.classifyForTest(descriptorAt(base + step)); got != int32(labeled.ID) {
		t.Errorf("classify after recognition = %d, want group %d", got, labeled.ID)
	}
}

func (f *detectorFixture) verify(t *testing.T, face *models.ImageFace) {
	t.Helper()
	if err := f.db.Model(face).Update("verified", true).Error; err != nil {
		t.Fatal(err)
	}
}

func recognizeUnlabeled(t *testing.T, f *detectorFixture, detector *faceDetector) []*models.ImageFace {
	t.Helper()
	var updated []*models.ImageFace
	err := f.db.Transaction(func(tx *gorm.DB) error {
		var err error
		updated, err = detector.RecognizeUnlabeledFaces(tx, f.user)
		return err
	})
	if err != nil {
		t.Fatal("RecognizeUnlabeledFaces error:", err)
	}
	return updated
}

func TestRecognizeUnlabeledFacesRespectsVerification(t *testing.T) {
	t.Run("leaves a confirmed face alone and flags the faces it moves", func(t *testing.T) {
		f := setupDetectorTest(t)
		anna := "Anna"
		labeled := f.group(t, &anna)
		f.face(t, labeled, 0, descriptorAt(base))

		confirmedGroup, otherGroup := f.group(t, nil), f.group(t, nil)
		confirmed := f.face(t, confirmedGroup, 1, descriptorAt(base+step))
		f.verify(t, confirmed)
		other := f.face(t, otherGroup, 2, descriptorAt(base-step))

		detector := f.initialize(t)
		updated := recognizeUnlabeled(t, f, detector)

		if len(updated) != 1 || updated[0].ID != other.ID {
			t.Fatalf("updated faces = %+v, want only face %d", updated, other.ID)
		}

		var storedConfirmed, storedOther models.ImageFace
		if err := f.db.First(&storedConfirmed, confirmed.ID).Error; err != nil {
			t.Fatal(err)
		}
		if err := f.db.First(&storedOther, other.ID).Error; err != nil {
			t.Fatal(err)
		}

		if storedConfirmed.FaceGroupID != confirmedGroup.ID || storedConfirmed.AssignedBy != models.FaceAssignmentDetector || storedConfirmed.NeedsReview {
			t.Errorf("confirmed face = group %d, %s, review %v; want untouched in group %d",
				storedConfirmed.FaceGroupID, storedConfirmed.AssignedBy, storedConfirmed.NeedsReview, confirmedGroup.ID)
		}
		if storedOther.FaceGroupID != labeled.ID || storedOther.AssignedBy != models.FaceAssignmentReclassify || !storedOther.NeedsReview {
			t.Errorf("moved face = group %d, %s, review %v; want group %d, RECLASSIFY, review true",
				storedOther.FaceGroupID, storedOther.AssignedBy, storedOther.NeedsReview, labeled.ID)
		}
	})

	t.Run("a confirmed unlabeled face is no reference", func(t *testing.T) {
		f := setupDetectorTest(t)
		confirmedGroup, otherGroup := f.group(t, nil), f.group(t, nil)
		confirmed := f.face(t, confirmedGroup, 0, descriptorAt(base))
		f.verify(t, confirmed)
		other := f.face(t, otherGroup, 1, descriptorAt(base+step))

		detector := f.initialize(t)
		if updated := recognizeUnlabeled(t, f, detector); len(updated) != 0 {
			t.Errorf("updated faces = %+v, want none", updated)
		}

		var stored models.ImageFace
		if err := f.db.First(&stored, other.ID).Error; err != nil {
			t.Fatal(err)
		}
		if stored.FaceGroupID != otherGroup.ID {
			t.Errorf("face is in group %d, want it to stay in %d", stored.FaceGroupID, otherGroup.ID)
		}

		// The confirmed face is back among the samples afterwards.
		if got := detector.classifyForTest(descriptorAt(base)); got != int32(confirmedGroup.ID) {
			t.Errorf("classify the confirmed face = %d, want group %d", got, confirmedGroup.ID)
		}
	})
}
