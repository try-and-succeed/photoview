package resolvers

import (
	"testing"

	"github.com/photoview/photoview/api/graphql/models"
)

func TestDetachImageFacesCleansUp(t *testing.T) {
	t.Run("deletes a group that detaching emptied", func(t *testing.T) {
		r, ctx, _ := setupFaceMutationTest(t)
		createFaceMutationFixtures(t, r)

		faces := []models.ImageFace{
			{Model: models.Model{ID: 1}, FaceGroupID: 1, MediaID: 1},
			{Model: models.Model{ID: 2}, FaceGroupID: 2, MediaID: 2},
		}
		if err := r.database.Create(&faces).Error; err != nil {
			t.Fatal(err)
		}

		newGroup, err := r.DetachImageFaces(ctx, []int{2})
		if err != nil {
			t.Fatal("DetachImageFaces error:", err)
		}
		if newGroup == nil || newGroup.ID == 2 {
			t.Fatalf("expected a new group, got %#v", newGroup)
		}

		var count int64
		if err := r.database.Model(&models.FaceGroup{}).Where("id = ?", 2).Count(&count).Error; err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Error("group 2 holds no face any more and should be deleted")
		}
	})

	t.Run("refuses an empty list instead of creating an empty group", func(t *testing.T) {
		r, ctx, _ := setupFaceMutationTest(t)
		createFaceMutationFixtures(t, r)

		var before int64
		if err := r.database.Model(&models.FaceGroup{}).Count(&before).Error; err != nil {
			t.Fatal(err)
		}

		if _, err := r.DetachImageFaces(ctx, []int{}); err == nil {
			t.Error("expected an error for an empty list")
		}

		var after int64
		if err := r.database.Model(&models.FaceGroup{}).Count(&after).Error; err != nil {
			t.Fatal(err)
		}
		if after != before {
			t.Errorf("face groups went from %d to %d, want no new group", before, after)
		}
	})
}
