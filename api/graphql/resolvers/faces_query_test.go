package resolvers

import (
	"context"
	"sort"
	"sync"
	"testing"

	"github.com/photoview/photoview/api/graphql/auth"
	"github.com/photoview/photoview/api/graphql/models"
)

// setupFaceQueryTest gives a non-admin user the fixture album and puts faces
// on it and on an album the user cannot see:
//
//	group 1: photo 1, photo 2 (user's album), photo 5 (other album)
//	group 2: photo 5 (other album) only
func setupFaceQueryTest(t *testing.T) (*Resolver, *models.User, context.Context) {
	t.Helper()
	r, _, _ := setupFaceMutationTest(t)
	createFaceMutationFixtures(t, r)
	linked, _ := setupNonAdminUserWithAlbum(t, r)

	// Linking the album filled linked.Albums; a request starts with a user
	// fresh from the database.
	user := &models.User{}
	if err := r.database.First(user, linked.ID).Error; err != nil {
		t.Fatal(err)
	}
	ctx := auth.AddUserToContext(context.Background(), user)

	other := models.Album{Title: "Other Album", Path: "/other-album"}
	if err := r.database.Create(&other).Error; err != nil {
		t.Fatal(err)
	}
	if err := r.database.Create(&models.Media{Model: models.Model{ID: 5}, Path: "test5", AlbumID: other.ID}).Error; err != nil {
		t.Fatal(err)
	}

	faces := []models.ImageFace{
		{Model: models.Model{ID: 1}, FaceGroupID: 1, MediaID: 1},
		{Model: models.Model{ID: 2}, FaceGroupID: 1, MediaID: 2},
		{Model: models.Model{ID: 3}, FaceGroupID: 1, MediaID: 5},
		{Model: models.Model{ID: 4}, FaceGroupID: 2, MediaID: 5},
	}
	if err := r.database.Create(&faces).Error; err != nil {
		t.Fatal(err)
	}

	return r.Resolver, user, ctx
}

func TestFaceQueriesStayInUserAlbums(t *testing.T) {
	r, user, ctx := setupFaceQueryTest(t)
	groupResolver := &faceGroupResolver{r}
	queryResolver := &queryResolver{r}
	group1 := &models.FaceGroup{Model: models.Model{ID: 1}}

	count, err := groupResolver.ImageFaceCount(ctx, group1)
	if err != nil {
		t.Fatal("ImageFaceCount error:", err)
	}
	if count != 2 {
		t.Errorf("ImageFaceCount = %d, want 2 (photo 5 is not the user's)", count)
	}

	imageFaces, err := groupResolver.ImageFaces(ctx, group1, nil, nil)
	if err != nil {
		t.Fatal("ImageFaces error:", err)
	}
	ids := make([]int, len(imageFaces))
	for i, face := range imageFaces {
		ids[i] = face.ID
	}
	sort.Ints(ids)
	if len(ids) != 2 || ids[0] != 1 || ids[1] != 2 {
		t.Errorf("ImageFaces = %v, want [1 2]", ids)
	}

	groups, err := queryResolver.MyFaceGroups(ctx, nil, nil)
	if err != nil {
		t.Fatal("MyFaceGroups error:", err)
	}
	if len(groups) != 1 || groups[0].ID != 1 {
		t.Errorf("MyFaceGroups returned %d groups, want only group 1", len(groups))
	}

	if group, err := queryResolver.FaceGroup(ctx, 1); err != nil || group.ID != 1 {
		t.Errorf("FaceGroup(1) = %v, %v; want group 1", group, err)
	}
	if group, err := queryResolver.FaceGroup(ctx, 2); err != nil || group.ID != 0 {
		t.Errorf("FaceGroup(2) = %v, %v; want nothing, it is only on another album", group, err)
	}

	// The resolvers of one request share the user and run concurrently, so
	// none may cache the albums on it.
	if len(user.Albums) != 0 {
		t.Errorf("the queries loaded %d albums into the shared user", len(user.Albums))
	}
}

// Many face groups are counted at once, as gqlgen resolves the fields of a
// list concurrently. Run with -race to see a data race on the shared user.
func TestImageFaceCountConcurrently(t *testing.T) {
	r, _, ctx := setupFaceQueryTest(t)
	groupResolver := &faceGroupResolver{r}

	want := map[int]int{1: 2, 2: 0}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		for id, expected := range want {
			wg.Add(1)
			go func() {
				defer wg.Done()
				count, err := groupResolver.ImageFaceCount(ctx, &models.FaceGroup{Model: models.Model{ID: id}})
				if err != nil {
					t.Errorf("ImageFaceCount(%d) error: %v", id, err)
					return
				}
				if count != expected {
					t.Errorf("ImageFaceCount(%d) = %d, want %d", id, count, expected)
				}
			}()
		}
	}
	wg.Wait()
}
