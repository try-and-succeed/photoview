package resolvers

import (
	"context"
	"testing"

	"github.com/photoview/photoview/api/graphql/models"
)

// setupVerifyTest gives the admin from setupFaceMutationTest the fixture album
// (queries that list faces or photos only look at the user's albums) and
// faces 1-4: 1 and 2 in group 1 on photos 1 and 2, 3 in group 2 on photo 2,
// 4 in group 3 on photo 3.
func setupVerifyTest(t *testing.T) (*mutationResolver, context.Context, *models.User) {
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

	faces := []models.ImageFace{
		{Model: models.Model{ID: 1}, FaceGroupID: 1, MediaID: 1},
		{Model: models.Model{ID: 2}, FaceGroupID: 1, MediaID: 2},
		{Model: models.Model{ID: 3}, FaceGroupID: 2, MediaID: 2},
		{Model: models.Model{ID: 4}, FaceGroupID: 3, MediaID: 3},
	}
	if err := r.database.Create(&faces).Error; err != nil {
		t.Fatal(err)
	}

	return r, ctx, user
}

func loadFace(t *testing.T, r *mutationResolver, id int) models.ImageFace {
	t.Helper()
	var face models.ImageFace
	if err := r.database.First(&face, id).Error; err != nil {
		t.Fatalf("image face %d: %v", id, err)
	}
	return face
}

func setFaceFlags(t *testing.T, r *mutationResolver, ids []int, updates map[string]any) {
	t.Helper()
	if err := r.database.Model(&models.ImageFace{}).Where("id IN ?", ids).Updates(updates).Error; err != nil {
		t.Fatal(err)
	}
}

func TestFaceVerificationDefaults(t *testing.T) {
	r, _, _ := setupVerifyTest(t)

	face := loadFace(t, r, 1)
	if face.Verified || face.NeedsReview || face.AssignedBy != models.FaceAssignmentDetector {
		t.Errorf("new face = verified %v, review %v, %s; want false, false, DETECTOR", face.Verified, face.NeedsReview, face.AssignedBy)
	}
}

func TestMoveAndDetachSetFaceFlags(t *testing.T) {
	t.Run("moving confirms the face", func(t *testing.T) {
		r, ctx, _ := setupVerifyTest(t)
		setFaceFlags(t, r, []int{4}, map[string]any{"needs_review": true, "assigned_by": models.FaceAssignmentReclassify})

		if _, err := r.MoveImageFaces(ctx, []int{4}, 2); err != nil {
			t.Fatal("MoveImageFaces error:", err)
		}

		face := loadFace(t, r, 4)
		if !face.Verified || face.NeedsReview || face.AssignedBy != models.FaceAssignmentUser {
			t.Errorf("moved face = verified %v, review %v, %s; want true, false, USER", face.Verified, face.NeedsReview, face.AssignedBy)
		}
	})

	t.Run("detaching takes a confirmation back", func(t *testing.T) {
		r, ctx, _ := setupVerifyTest(t)
		setFaceFlags(t, r, []int{1}, map[string]any{"verified": true})

		if _, err := r.DetachImageFaces(ctx, []int{1}); err != nil {
			t.Fatal("DetachImageFaces error:", err)
		}

		face := loadFace(t, r, 1)
		if face.Verified || face.NeedsReview || face.AssignedBy != models.FaceAssignmentUser {
			t.Errorf("detached face = verified %v, review %v, %s; want false, false, USER", face.Verified, face.NeedsReview, face.AssignedBy)
		}
	})
}
