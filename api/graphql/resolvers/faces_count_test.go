package resolvers

import (
	"fmt"
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
		groups, err := queryRes.MyFaceGroups(ctx, nil, nil)
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

func TestMyFaceGroupsCountDirection(t *testing.T) {
	r, ctx, user := setupFaceMutationTest(t)
	createFaceMutationFixtures(t, r)

	var album models.Album
	if err := r.database.Where("path = ?", "/test-album").First(&album).Error; err != nil {
		t.Fatal("find fixture album error:", err)
	}
	if err := r.database.Model(user).Association("Albums").Append(&album); err != nil {
		t.Fatal("link user to album error:", err)
	}

	// The fixtures hold groups 1 to 4; 5 ties with 4 on the count.
	if err := r.database.Create(&models.FaceGroup{Model: models.Model{ID: 5}}).Error; err != nil {
		t.Fatal(err)
	}
	if err := r.database.Model(&models.FaceGroup{}).Where("id = ?", 1).Update("label", "Anna").Error; err != nil {
		t.Fatal(err)
	}

	rect := models.FaceRectangle{MinX: 0.1, MaxX: 0.2, MinY: 0.1, MaxY: 0.2}
	photosPerGroup := map[int][]int{
		1: {1},       // named
		2: {1},       // unnamed, 1 photo
		3: {1, 2, 3}, // unnamed, 3 photos
		4: {1, 2},    // unnamed, 2 photos
		5: {3, 4},    // unnamed, 2 photos
	}
	for group, mediaIDs := range photosPerGroup {
		for _, mediaID := range mediaIDs {
			face := models.ImageFace{FaceGroupID: group, MediaID: mediaID, Rectangle: rect}
			if err := r.database.Create(&face).Error; err != nil {
				t.Fatal(err)
			}
		}
	}

	queryRes := &queryResolver{r.Resolver}
	list := func(t *testing.T, paginate *models.Pagination, direction *models.OrderDirection) []int {
		t.Helper()
		groups, err := queryRes.MyFaceGroups(ctx, paginate, direction)
		if err != nil {
			t.Fatal("MyFaceGroups error:", err)
		}
		ids := make([]int, len(groups))
		for i, group := range groups {
			ids[i] = group.ID
		}
		return ids
	}
	asc, desc := models.OrderDirectionAsc, models.OrderDirectionDesc

	// Named first in either direction; equal counts by ascending id.
	for _, tc := range []struct {
		name      string
		direction *models.OrderDirection
		want      string
	}{
		{"DESC", &desc, "[1 3 4 5 2]"},
		{"ASC", &asc, "[1 2 4 5 3]"},
		{"default", nil, "[1 3 4 5 2]"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := fmt.Sprint(list(t, nil, tc.direction)); got != tc.want {
				t.Errorf("myFaceGroups order = %s, want %s", got, tc.want)
			}
		})
	}

	t.Run("pages of one in ASC hold every group once", func(t *testing.T) {
		var got []int
		for offset := 0; offset < 6; offset++ {
			limit, offset := 1, offset
			got = append(got, list(t, &models.Pagination{Limit: &limit, Offset: &offset}, &asc)...)
		}
		if fmt.Sprint(got) != "[1 2 4 5 3]" {
			t.Errorf("paged myFaceGroups = %v, want [1 2 4 5 3]", got)
		}
	})

	t.Run("a limit of 0 returns nothing, as the app's probe expects", func(t *testing.T) {
		limit := 0
		if got := list(t, &models.Pagination{Limit: &limit}, &asc); len(got) != 0 {
			t.Errorf("myFaceGroups with limit 0 = %v, want none", got)
		}
	})
}
