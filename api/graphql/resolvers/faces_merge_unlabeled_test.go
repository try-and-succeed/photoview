package resolvers

import (
	"reflect"
	"testing"

	"github.com/photoview/photoview/api/graphql/models"
	"gorm.io/gorm"
)

func mergeDescriptorAt(value float32) models.FaceDescriptor {
	var d models.FaceDescriptor
	for i := range d {
		d[i] = value
	}
	return d
}

// 128 * 0.0342^2 = 0.15: within the tolerance of 0.2, two steps (0.6) not.
const mergeStep = float32(0.0342)

type mergeFixture struct {
	db   *gorm.DB
	user *models.User
}

// setupMergeTest gives the user photos 1-6; photo 7 is in an album the user
// cannot see.
func setupMergeTest(t *testing.T) *mergeFixture {
	t.Helper()
	r, _, user := setupFaceMutationTest(t)
	createFaceMutationFixtures(t, r)
	db := r.database

	var album models.Album
	if err := db.Where("path = ?", "/test-album").First(&album).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(user).Association("Albums").Append(&album); err != nil {
		t.Fatal(err)
	}
	foreign := models.Album{Title: "Foreign", Path: "/foreign-album"}
	if err := db.Create(&foreign).Error; err != nil {
		t.Fatal(err)
	}
	media := []models.Media{
		{Model: models.Model{ID: 5}, Path: "test5", AlbumID: album.ID},
		{Model: models.Model{ID: 6}, Path: "test6", AlbumID: album.ID},
		{Model: models.Model{ID: 7}, Path: "test7", AlbumID: foreign.ID},
	}
	if err := db.Create(&media).Error; err != nil {
		t.Fatal(err)
	}

	return &mergeFixture{db: db, user: user}
}

func (f *mergeFixture) groups(t *testing.T, labels map[int]string, ids ...int) {
	t.Helper()
	for _, id := range ids {
		group := models.FaceGroup{Model: models.Model{ID: id}}
		if label, ok := labels[id]; ok {
			group.Label = &label
		}
		if err := f.db.Create(&group).Error; err != nil {
			t.Fatal(err)
		}
	}
}

func (f *mergeFixture) faces(t *testing.T, faces ...models.ImageFace) {
	t.Helper()
	if err := f.db.Create(&faces).Error; err != nil {
		t.Fatal(err)
	}
}

func mergeFace(id, group, media int, at float32, verified bool) models.ImageFace {
	return models.ImageFace{
		Model:       models.Model{ID: id},
		FaceGroupID: group,
		MediaID:     media,
		Descriptor:  mergeDescriptorAt(at),
		Verified:    verified,
	}
}

func (f *mergeFixture) snapshot(t *testing.T) ([]models.ImageFace, []int) {
	t.Helper()
	var faces []models.ImageFace
	if err := f.db.Order("id").Find(&faces).Error; err != nil {
		t.Fatal(err)
	}
	var groupIDs []int
	if err := f.db.Model(&models.FaceGroup{}).Order("id").Pluck("id", &groupIDs).Error; err != nil {
		t.Fatal(err)
	}
	return faces, groupIDs
}

func (f *mergeFixture) run(t *testing.T, dryRun bool) []*models.FaceGroupMerge {
	t.Helper()
	merges, err := mergeSimilarUnlabeledFaceGroups(f.db, f.user, dryRun, nil)
	if err != nil {
		t.Fatal("mergeSimilarUnlabeledFaceGroups error:", err)
	}
	return merges
}

// setupMergeScenario builds the groups the main tests share:
//
//	10  photo 1  0                  -> merges with 11
//	11  photo 2  0 + step           -> into 10
//	13  photo 1  0 - 0.8 step       nearest to 10, but 10 is on photo 1 already
//	14  photo 4  0.5 (confirmed), photo 5 0.5 + step/2   never takes part
//	15  photo 6  0.5 + step         its only neighbours are in group 14
//	16  Anna, photo 4  -0.5         named, never takes part
//	17  photo 5  -0.5 + step        its only neighbour is Anna
//	18  photo 6 and photo 7 (foreign album)  1.0   never takes part
//	19  photo 2  1.0 + step         its only neighbours are in group 18
func setupMergeScenario(t *testing.T) *mergeFixture {
	t.Helper()
	f := setupMergeTest(t)
	f.groups(t, map[int]string{16: "Anna"}, 10, 11, 13, 14, 15, 16, 17, 18, 19)
	f.faces(t,
		mergeFace(100, 10, 1, 0, false),
		mergeFace(101, 11, 2, mergeStep, false),
		mergeFace(103, 13, 1, -0.8*mergeStep, false),
		mergeFace(104, 14, 4, 0.5, true),
		mergeFace(105, 14, 5, 0.5+mergeStep/2, false),
		mergeFace(106, 15, 6, 0.5+mergeStep, false),
		mergeFace(107, 16, 4, -0.5, false),
		mergeFace(108, 17, 5, -0.5+mergeStep, false),
		mergeFace(109, 18, 6, 1.0, false),
		mergeFace(110, 18, 7, 1.0, false),
		mergeFace(111, 19, 2, 1.0+mergeStep, false),
	)
	return f
}

func mergeFaceIDs(faces []*models.ImageFace) []int {
	ids := make([]int, len(faces))
	for i, face := range faces {
		ids[i] = face.ID
	}
	return ids
}

func TestMergeSimilarUnlabeledFaceGroups(t *testing.T) {
	t.Run("suggests only the merge that keeps one person per photo", func(t *testing.T) {
		f := setupMergeScenario(t)
		beforeFaces, beforeGroups := f.snapshot(t)

		merges := f.run(t, true)

		if len(merges) != 1 {
			t.Fatalf("merges = %d, want 1", len(merges))
		}
		m := merges[0]
		if m.Destination.ID != 10 || !reflect.DeepEqual(m.SourceFaceGroupIDs, []int{11}) {
			t.Errorf("merge = %d <- %v, want 10 <- [11]", m.Destination.ID, m.SourceFaceGroupIDs)
		}
		if got := mergeFaceIDs(m.ImageFaces); !reflect.DeepEqual(got, []int{100, 101}) {
			t.Errorf("faces = %v, want [100 101]", got)
		}

		afterFaces, afterGroups := f.snapshot(t)
		if !reflect.DeepEqual(beforeFaces, afterFaces) || !reflect.DeepEqual(beforeGroups, afterGroups) {
			t.Error("a dry run changed the database")
		}
	})

	t.Run("merges, flags the faces for review and confirms none", func(t *testing.T) {
		f := setupMergeScenario(t)
		beforeFaces, _ := f.snapshot(t)

		merges := f.run(t, false)

		if len(merges) != 1 || merges[0].Destination.ID != 10 {
			t.Fatalf("merges = %+v, want one into group 10", merges)
		}
		for _, face := range merges[0].ImageFaces {
			if face.FaceGroupID != 10 || !face.NeedsReview {
				t.Errorf("returned face %d = group %d, review %v; want group 10, review", face.ID, face.FaceGroupID, face.NeedsReview)
			}
		}

		afterFaces, afterGroups := f.snapshot(t)
		for i, face := range afterFaces {
			before := beforeFaces[i]
			switch face.ID {
			case 100:
				if face.FaceGroupID != 10 || face.AssignedBy != models.FaceAssignmentDetector || !face.NeedsReview || face.Verified {
					t.Errorf("face 100 = group %d, %s, review %v, verified %v; want group 10, DETECTOR, review, unconfirmed",
						face.FaceGroupID, face.AssignedBy, face.NeedsReview, face.Verified)
				}
			case 101:
				if face.FaceGroupID != 10 || face.AssignedBy != models.FaceAssignmentReclassify || !face.NeedsReview || face.Verified {
					t.Errorf("face 101 = group %d, %s, review %v, verified %v; want group 10, RECLASSIFY, review, unconfirmed",
						face.FaceGroupID, face.AssignedBy, face.NeedsReview, face.Verified)
				}
			default:
				if face.FaceGroupID != before.FaceGroupID || face.NeedsReview != before.NeedsReview || face.AssignedBy != before.AssignedBy {
					t.Errorf("face %d changed: group %d -> %d, review %v -> %v", face.ID,
						before.FaceGroupID, face.FaceGroupID, before.NeedsReview, face.NeedsReview)
				}
			}
		}
		for _, id := range afterGroups {
			if id == 11 {
				t.Error("the merged group 11 should be deleted")
			}
		}

		if again := f.run(t, true); len(again) != 0 {
			t.Errorf("a second run suggests %d merges, want none", len(again))
		}
	})

	t.Run("the group with the most faces takes in the others", func(t *testing.T) {
		f := setupMergeTest(t)
		f.groups(t, nil, 20, 21)
		f.faces(t,
			mergeFace(200, 20, 1, 2.0, false),
			mergeFace(201, 21, 2, 2.0+mergeStep, false),
			mergeFace(202, 21, 3, 2.0+mergeStep, false),
		)

		merges := f.run(t, true)
		if len(merges) != 1 || merges[0].Destination.ID != 21 || !reflect.DeepEqual(merges[0].SourceFaceGroupIDs, []int{20}) {
			t.Fatalf("merges = %+v, want 21 <- [20]", merges)
		}
	})

	t.Run("a user without albums gets nothing", func(t *testing.T) {
		f := setupMergeScenario(t)
		pass := "5678"
		other, err := models.RegisterUser(f.db, "no_albums", &pass, false)
		if err != nil {
			t.Fatal(err)
		}
		merges, err := mergeSimilarUnlabeledFaceGroups(f.db, other, false, nil)
		if err != nil {
			t.Fatal(err)
		}
		if len(merges) != 0 {
			t.Errorf("merges = %d, want none", len(merges))
		}
	})

	t.Run("rejects a tolerance of 0", func(t *testing.T) {
		f := setupMergeTest(t)
		zero := 0.0
		if _, err := mergeSimilarUnlabeledFaceGroups(f.db, f.user, true, &zero); err == nil {
			t.Error("want an error for tolerance 0")
		}
	})
}
