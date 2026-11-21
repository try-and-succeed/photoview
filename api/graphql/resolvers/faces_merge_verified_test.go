package resolvers

import (
	"context"
	"testing"

	"github.com/photoview/photoview/api/graphql/models"
)

func TestCombineFaceGroupsKeepsConfirmedFaces(t *testing.T) {
	// Group 1 is the destination, group 2 the source; both show photo 2.
	setup := func(t *testing.T) (*mutationResolver, context.Context, func(ids ...int)) {
		t.Helper()
		r, ctx, _ := setupFaceMutationTest(t)
		createFaceMutationFixtures(t, r)

		faces := []models.ImageFace{
			{Model: models.Model{ID: 1}, FaceGroupID: 1, MediaID: 1},
			{Model: models.Model{ID: 2}, FaceGroupID: 1, MediaID: 2},
			{Model: models.Model{ID: 3}, FaceGroupID: 2, MediaID: 2},
			{Model: models.Model{ID: 4}, FaceGroupID: 2, MediaID: 3},
		}
		if err := r.database.Create(&faces).Error; err != nil {
			t.Fatal(err)
		}

		verify := func(ids ...int) {
			if err := r.database.Model(&models.ImageFace{}).Where("id IN ?", ids).Update("verified", true).Error; err != nil {
				t.Fatal(err)
			}
		}
		return r, ctx, verify
	}

	load := func(t *testing.T, r *mutationResolver, id int) models.ImageFace {
		t.Helper()
		var face models.ImageFace
		if err := r.database.First(&face, id).Error; err != nil {
			t.Fatal(err)
		}
		return face
	}

	merge := func(t *testing.T, r *mutationResolver, ctx context.Context) {
		t.Helper()
		if _, err := r.CombineFaceGroups(ctx, 1, []int{2}); err != nil {
			t.Fatal("CombineFaceGroups error:", err)
		}
	}

	t.Run("a confirmed source face wins the shared photo", func(t *testing.T) {
		r, ctx, verify := setup(t)
		verify(3)
		merge(t, r, ctx)

		assertFaceGroupMembers(t, r, 1, []int{1, 3, 4})
		if face := load(t, r, 3); !face.Verified {
			t.Error("the confirmed face should stay confirmed")
		}

		detached := load(t, r, 2)
		if detached.FaceGroupID == 1 {
			t.Fatal("the destination's unconfirmed face should be detached")
		}
		if !detached.NeedsReview {
			t.Error("the detached face should be up for review")
		}
	})

	t.Run("without confirmations the destination keeps the photo", func(t *testing.T) {
		r, ctx, _ := setup(t)
		merge(t, r, ctx)

		assertFaceGroupMembers(t, r, 1, []int{1, 2, 4})
		detached := load(t, r, 3)
		if detached.Verified || !detached.NeedsReview {
			t.Errorf("detached face = verified %v, review %v; want false, true", detached.Verified, detached.NeedsReview)
		}
	})

	t.Run("when both are confirmed the destination wins and the other loses its confirmation", func(t *testing.T) {
		r, ctx, verify := setup(t)
		verify(2, 3)
		merge(t, r, ctx)

		assertFaceGroupMembers(t, r, 1, []int{1, 2, 4})
		detached := load(t, r, 3)
		if detached.FaceGroupID == 1 {
			t.Fatal("face 3 should be detached")
		}
		if detached.Verified || !detached.NeedsReview {
			t.Errorf("detached face = verified %v, review %v; want false, true", detached.Verified, detached.NeedsReview)
		}
	})
}
