package resolvers

import (
	"context"
	"testing"

	"github.com/photoview/photoview/api/graphql/models"
)

func faceDescriptorAt(value float32) models.FaceDescriptor {
	var d models.FaceDescriptor
	for i := range d {
		d[i] = value
	}
	return d
}

const (
	henrik = float32(0.05)
	meike  = float32(-0.05)
	// 128 * 0.0342^2 = 0.15: within the tolerance of 0.2, two steps (0.6) not.
	reclassifyStep = float32(0.0342)
)

type reclassifyFixture struct {
	r   *mutationResolver
	ctx context.Context
}

// setupReclassifyTest builds the scenario the tests share. Group 1 is Meike,
// group 2 Henrik, group 3 unnamed; the user owns photos 1-4.
//
//	10  Henrik  photo 1  Henrik's face      verified
//	11  Meike   photo 1  Meike's face       verified
//	25  Meike   photo 4  Meike's face       verified
//	20  Meike   photo 2  near Henrik        -> moves to Henrik
//	21  Meike   photo 3  near Meike         -> kept
//	22  Meike   photo 4  far from everyone  -> unmatched, own group
//	23  Meike   photo 1  Henrik's face      -> conflict: Henrik is on photo 1
//	24  Meike   photo 2  near face 20 only  -> unmatched: references stay fixed
//	26  unnamed photo 3  Henrik's face      -> moves to Henrik, group 3 empties
func setupReclassifyTest(t *testing.T) *reclassifyFixture {
	t.Helper()
	r, ctx, user := setupFaceMutationTest(t)
	createFaceMutationFixtures(t, r)

	var album models.Album
	if err := r.database.Where("path = ?", "/test-album").First(&album).Error; err != nil {
		t.Fatal("find fixture album error:", err)
	}
	if err := r.database.Model(user).Association("Albums").Append(&album); err != nil {
		t.Fatal("link user to album error:", err)
	}

	for id, label := range map[int]string{1: "Meike", 2: "Henrik"} {
		if err := r.database.Model(&models.FaceGroup{}).Where("id = ?", id).Update("label", label).Error; err != nil {
			t.Fatal(err)
		}
	}

	face := func(id, group, media int, descriptor float32, verified bool) models.ImageFace {
		return models.ImageFace{
			Model:       models.Model{ID: id},
			FaceGroupID: group,
			MediaID:     media,
			Descriptor:  faceDescriptorAt(descriptor),
			Verified:    verified,
		}
	}
	faces := []models.ImageFace{
		face(10, 2, 1, henrik, true),
		face(11, 1, 1, meike, true),
		face(25, 1, 4, meike, true),
		face(20, 1, 2, henrik+reclassifyStep, false),
		face(21, 1, 3, meike+0.005, false),
		face(22, 1, 4, 0.3, false),
		face(23, 1, 1, henrik, false),
		face(24, 1, 2, henrik+2*reclassifyStep, false),
		face(26, 3, 3, henrik, false),
	}
	if err := r.database.Create(&faces).Error; err != nil {
		t.Fatal(err)
	}

	return &reclassifyFixture{r: r, ctx: ctx}
}

func (f *reclassifyFixture) face(t *testing.T, id int) models.ImageFace {
	t.Helper()
	var face models.ImageFace
	if err := f.r.database.First(&face, id).Error; err != nil {
		t.Fatalf("image face %d: %v", id, err)
	}
	return face
}

func (f *reclassifyFixture) groupExists(t *testing.T, id int) bool {
	t.Helper()
	var count int64
	if err := f.r.database.Model(&models.FaceGroup{}).Where("id = ?", id).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	return count == 1
}

func (f *reclassifyFixture) snapshot(t *testing.T) []models.ImageFace {
	t.Helper()
	var faces []models.ImageFace
	if err := f.r.database.Order("id").Find(&faces).Error; err != nil {
		t.Fatal(err)
	}
	return faces
}

func checkCounts(t *testing.T, got *models.ReclassifyResult) {
	t.Helper()
	want := models.ReclassifyResult{Checked: 6, Kept: 1, Moved: 2, Unmatched: 2, Conflicts: 1, VerifiedSamples: 3}
	if got.Checked != want.Checked || got.Kept != want.Kept || got.Moved != want.Moved ||
		got.Unmatched != want.Unmatched || got.Conflicts != want.Conflicts || got.VerifiedSamples != want.VerifiedSamples {
		t.Errorf("result = checked %d kept %d moved %d unmatched %d conflicts %d samples %d; want 6 1 2 2 1 3",
			got.Checked, got.Kept, got.Moved, got.Unmatched, got.Conflicts, got.VerifiedSamples)
	}
}

func TestReclassifyFaceGroups(t *testing.T) {
	t.Run("a real run", func(t *testing.T) {
		f := setupReclassifyTest(t)

		result, err := f.r.ReclassifyFaceGroups(f.ctx, []int{1, 3}, false, nil)
		if err != nil {
			t.Fatal("ReclassifyFaceGroups error:", err)
		}
		checkCounts(t, result)

		// Moved, with the run's marks.
		for _, id := range []int{20, 26} {
			if face := f.face(t, id); face.FaceGroupID != 2 || face.AssignedBy != models.FaceAssignmentReclassify || !face.NeedsReview {
				t.Errorf("face %d = group %d, %s, review %v; want group 2, RECLASSIFY, review", id, face.FaceGroupID, face.AssignedBy, face.NeedsReview)
			}
		}
		// Kept and conflict stay, but are up for review as well.
		for _, id := range []int{21, 23} {
			if face := f.face(t, id); face.FaceGroupID != 1 || face.AssignedBy != models.FaceAssignmentReclassify || !face.NeedsReview {
				t.Errorf("face %d = group %d, %s, review %v; want group 1, RECLASSIFY, review", id, face.FaceGroupID, face.AssignedBy, face.NeedsReview)
			}
		}
		// Unmatched: each in a new group of its own, the chained one too.
		g22, g24 := f.face(t, 22).FaceGroupID, f.face(t, 24).FaceGroupID
		if g22 <= 4 || g24 <= 4 || g22 == g24 {
			t.Errorf("unmatched faces are in groups %d and %d, want two new groups", g22, g24)
		}
		for _, id := range []int{22, 24} {
			if face := f.face(t, id); !face.NeedsReview || face.AssignedBy != models.FaceAssignmentReclassify {
				t.Errorf("face %d = %s, review %v; want RECLASSIFY, review", id, face.AssignedBy, face.NeedsReview)
			}
		}
		// Verified faces are never touched.
		for _, id := range []int{10, 11, 25} {
			if face := f.face(t, id); !face.Verified || face.NeedsReview || face.AssignedBy != models.FaceAssignmentDetector {
				t.Errorf("verified face %d = verified %v, review %v, %s; want untouched", id, face.Verified, face.NeedsReview, face.AssignedBy)
			}
		}
		// Group 3 lost its only face; Meike keeps hers.
		if f.groupExists(t, 3) {
			t.Error("group 3 is empty and should be deleted")
		}
		if !f.groupExists(t, 1) {
			t.Error("group 1 still has faces and should stay")
		}

		// changes: the moved and the unmatched faces, with their new groups.
		wantTo := map[int]int{20: 2, 22: g22, 24: g24, 26: 2}
		if len(result.Changes) != len(wantTo) {
			t.Fatalf("got %d changes, want %d", len(result.Changes), len(wantTo))
		}
		for _, change := range result.Changes {
			want, ok := wantTo[change.ImageFace.ID]
			if !ok {
				t.Errorf("unexpected change for face %d", change.ImageFace.ID)
				continue
			}
			if change.To == nil || change.To.ID != want {
				t.Errorf("change for face %d goes to %v, want group %d", change.ImageFace.ID, change.To, want)
			}
			if change.ImageFace.FaceGroupID != want {
				t.Errorf("change for face %d reports the face in group %d, want %d", change.ImageFace.ID, change.ImageFace.FaceGroupID, want)
			}
		}
	})

	t.Run("dryRun writes nothing", func(t *testing.T) {
		f := setupReclassifyTest(t)
		before := f.snapshot(t)
		var groupsBefore int64
		f.r.database.Model(&models.FaceGroup{}).Count(&groupsBefore)

		result, err := f.r.ReclassifyFaceGroups(f.ctx, []int{1, 3}, true, nil)
		if err != nil {
			t.Fatal("ReclassifyFaceGroups error:", err)
		}
		checkCounts(t, result)

		after := f.snapshot(t)
		for i := range before {
			b, a := before[i], after[i]
			if b.FaceGroupID != a.FaceGroupID || b.Verified != a.Verified || b.NeedsReview != a.NeedsReview || b.AssignedBy != a.AssignedBy {
				t.Errorf("face %d changed: %+v -> %+v", b.ID, b, a)
			}
		}
		var groupsAfter int64
		f.r.database.Model(&models.FaceGroup{}).Count(&groupsAfter)
		if groupsAfter != groupsBefore {
			t.Errorf("face groups went from %d to %d", groupsBefore, groupsAfter)
		}

		for _, change := range result.Changes {
			switch change.ImageFace.ID {
			case 20, 26:
				if change.To == nil || change.To.ID != 2 {
					t.Errorf("face %d would go to %v, want group 2", change.ImageFace.ID, change.To)
				}
			case 22, 24:
				if change.To != nil {
					t.Errorf("face %d: to = group %d, want null with dryRun", change.ImageFace.ID, change.To.ID)
				}
			default:
				t.Errorf("unexpected change for face %d", change.ImageFace.ID)
			}
		}
	})

	t.Run("no verified faces is an error", func(t *testing.T) {
		f := setupReclassifyTest(t)
		if err := f.r.database.Model(&models.ImageFace{}).Where("verified = ?", true).Update("verified", false).Error; err != nil {
			t.Fatal(err)
		}
		if _, err := f.r.ReclassifyFaceGroups(f.ctx, []int{1}, true, nil); err == nil {
			t.Error("expected an error without verified faces")
		}
	})

	t.Run("an empty list does nothing", func(t *testing.T) {
		f := setupReclassifyTest(t)
		result, err := f.r.ReclassifyFaceGroups(f.ctx, []int{}, false, nil)
		if err != nil {
			t.Fatal("ReclassifyFaceGroups error:", err)
		}
		if result.Checked != 0 || len(result.Changes) != 0 {
			t.Errorf("result = %+v, want nothing checked", result)
		}
	})

	t.Run("the tolerance must be positive", func(t *testing.T) {
		f := setupReclassifyTest(t)
		zero := 0.0
		if _, err := f.r.ReclassifyFaceGroups(f.ctx, []int{1}, true, &zero); err == nil {
			t.Error("expected an error for a tolerance of 0")
		}
	})

	t.Run("a non-admin cannot reclassify a group outside their albums", func(t *testing.T) {
		f := setupReclassifyTest(t)
		_, userCtx := setupNonAdminUserWithAlbum(t, f.r)

		other := models.Album{Title: "other", Path: "/other-album"}
		if err := f.r.database.Create(&other).Error; err != nil {
			t.Fatal(err)
		}
		media := models.Media{Model: models.Model{ID: 5}, Path: "other", AlbumID: other.ID}
		if err := f.r.database.Create(&media).Error; err != nil {
			t.Fatal(err)
		}
		if err := f.r.database.Create(&models.ImageFace{Model: models.Model{ID: 30}, FaceGroupID: 4, MediaID: 5}).Error; err != nil {
			t.Fatal(err)
		}

		if _, err := f.r.ReclassifyFaceGroups(userCtx, []int{4}, false, nil); err == nil {
			t.Error("expected an error for a group outside the user's albums")
		}
		if face := f.face(t, 30); face.FaceGroupID != 4 || face.NeedsReview {
			t.Errorf("face 30 = group %d, review %v; want untouched", face.FaceGroupID, face.NeedsReview)
		}
	})
}
