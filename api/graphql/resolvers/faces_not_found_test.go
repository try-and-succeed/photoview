package resolvers

import (
	"errors"
	"testing"

	"github.com/photoview/photoview/api/graphql/models"
)

// A face group that does not exist, or that a user cannot see, is an error,
// not an empty group with ID 0 the mutations go on with.
func TestFaceGroupNotFound(t *testing.T) {
	const missing = 999999

	setup := func(t *testing.T) *mutationResolver {
		t.Helper()
		r, _, _ := setupFaceMutationTest(t)
		createFaceMutationFixtures(t, r)
		if err := r.database.Create(&models.ImageFace{Model: models.Model{ID: 1}, FaceGroupID: 1, MediaID: 1}).Error; err != nil {
			t.Fatal(err)
		}
		return r
	}

	faceGroupOf := func(t *testing.T, r *mutationResolver, imageFaceID int) int {
		t.Helper()
		var face models.ImageFace
		if err := r.database.First(&face, imageFaceID).Error; err != nil {
			t.Fatal(err)
		}
		return face.FaceGroupID
	}

	expectNotFound := func(t *testing.T, err error) {
		t.Helper()
		if !errors.Is(err, ErrFaceGroupNotFound) {
			t.Errorf("error = %v, want %v", err, ErrFaceGroupNotFound)
		}
	}

	t.Run("an admin and a missing group", func(t *testing.T) {
		r, ctx, _ := setupFaceMutationTest(t)
		createFaceMutationFixtures(t, r)
		if err := r.database.Create(&models.ImageFace{Model: models.Model{ID: 1}, FaceGroupID: 1, MediaID: 1}).Error; err != nil {
			t.Fatal(err)
		}

		_, err := r.MoveImageFaces(ctx, []int{1}, missing, nil)
		expectNotFound(t, err)
		if group := faceGroupOf(t, r, 1); group != 1 {
			t.Errorf("face 1 is in group %d, want it to stay in group 1", group)
		}

		_, err = r.CombineFaceGroups(ctx, missing, []int{1})
		expectNotFound(t, err)
		_, err = r.CombineFaceGroups(ctx, 1, []int{missing})
		expectNotFound(t, err)
		_, err = r.SetFaceGroupLabel(ctx, missing, nil)
		expectNotFound(t, err)

		_, err = (&queryResolver{r.Resolver}).FaceGroup(ctx, missing)
		expectNotFound(t, err)
	})

	t.Run("a user and a group outside their albums", func(t *testing.T) {
		r := setup(t)
		_, userCtx := setupNonAdminUserWithAlbum(t, r)

		other := models.Album{Title: "Other Album", Path: "/other-album"}
		if err := r.database.Create(&other).Error; err != nil {
			t.Fatal(err)
		}
		if err := r.database.Create(&models.Media{Model: models.Model{ID: 5}, Path: "test5", AlbumID: other.ID}).Error; err != nil {
			t.Fatal(err)
		}
		if err := r.database.Create(&models.ImageFace{Model: models.Model{ID: 2}, FaceGroupID: 2, MediaID: 5}).Error; err != nil {
			t.Fatal(err)
		}

		_, err := r.MoveImageFaces(userCtx, []int{1}, 2, nil)
		expectNotFound(t, err)
		if group := faceGroupOf(t, r, 1); group != 1 {
			t.Errorf("face 1 is in group %d, want it to stay in group 1", group)
		}

		label := "Someone"
		_, err = r.SetFaceGroupLabel(userCtx, 2, &label)
		expectNotFound(t, err)
		var group models.FaceGroup
		if err := r.database.First(&group, 2).Error; err != nil {
			t.Fatal(err)
		}
		if group.Label != nil {
			t.Errorf("group 2 got the label %q", *group.Label)
		}

		_, err = (&queryResolver{r.Resolver}).FaceGroup(userCtx, 2)
		expectNotFound(t, err)

		// The user's own group is still found.
		if found, err := (&queryResolver{r.Resolver}).FaceGroup(userCtx, 1); err != nil || found.ID != 1 {
			t.Errorf("FaceGroup(1) = %v, %v; want group 1", found, err)
		}
	})
}
