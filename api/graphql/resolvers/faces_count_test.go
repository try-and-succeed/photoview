package resolvers

import (
	"testing"

	"github.com/photoview/photoview/api/graphql/models"
)

func TestFaceGroupsCountPhotos(t *testing.T) {
	r, ctx, user := setupFaceMutationTest(t)
	createFaceMutationFixtures(t, r)

	var album models.Album
	if err := r.database.Where("path = ?", "/test-album").First(&album).Error; err != nil {
		t.Fatal("find fixture album error:", err)
	}
	if err := r.database.Model(user).Association("Albums").Append(&album); err != nil {
		t.Fatal("link user to album error:", err)
	}

	rect := func(x float64) models.FaceRectangle {
		return models.FaceRectangle{MinX: x, MaxX: x + 0.1, MinY: 0.1, MaxY: 0.2}
	}
	// Group 1: four faces on two photos - two people the recognizer filed
	// together on each. Group 2: one face on each of three photos.
	faces := []models.ImageFace{
		{FaceGroupID: 1, MediaID: 1, Rectangle: rect(0.1)},
		{FaceGroupID: 1, MediaID: 1, Rectangle: rect(0.3)},
		{FaceGroupID: 1, MediaID: 2, Rectangle: rect(0.1)},
		{FaceGroupID: 1, MediaID: 2, Rectangle: rect(0.3)},
		{FaceGroupID: 2, MediaID: 2, Rectangle: rect(0.5)},
		{FaceGroupID: 2, MediaID: 3, Rectangle: rect(0.5)},
		{FaceGroupID: 2, MediaID: 4, Rectangle: rect(0.5)},
	}
	if err := r.database.Create(&faces).Error; err != nil {
		t.Fatal(err)
	}

	t.Run("imageFaceCount counts each photo once", func(t *testing.T) {
		groupResolver := &faceGroupResolver{r.Resolver}
		for _, tc := range []struct{ group, want int }{{1, 2}, {2, 3}} {
			got, err := groupResolver.ImageFaceCount(ctx, &models.FaceGroup{Model: models.Model{ID: tc.group}})
			if err != nil {
				t.Fatal("ImageFaceCount error:", err)
			}
			if got != tc.want {
				t.Errorf("imageFaceCount of group %d = %d, want %d", tc.group, got, tc.want)
			}
		}
	})

	t.Run("myFaceGroups orders by photos", func(t *testing.T) {
		queryRes := &queryResolver{r.Resolver}
		groups, err := queryRes.MyFaceGroups(ctx, nil)
		if err != nil {
			t.Fatal("MyFaceGroups error:", err)
		}

		got := make([]int, len(groups))
		for i, group := range groups {
			got[i] = group.ID
		}
		if len(got) != 2 || got[0] != 2 || got[1] != 1 {
			t.Errorf("myFaceGroups order = %v, want [2 1]", got)
		}
	})
}
