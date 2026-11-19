package resolvers

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/photoview/photoview/api/database/drivers"
	"github.com/photoview/photoview/api/graphql/auth"
	"github.com/photoview/photoview/api/graphql/models"
	"github.com/photoview/photoview/api/scanner/face_detection"
	"github.com/photoview/photoview/api/test_utils"
)

func setupFaceMutationTest(t *testing.T) (*mutationResolver, context.Context, *models.User) {
	t.Helper()

	test_utils.FilesystemTest(t)
	db := test_utils.DatabaseTest(t)
	face_detection.InitializeFaceDetector(db)

	pass := "1234"
	user, err := models.RegisterUser(db, "test_user", &pass, true)
	if err != nil {
		t.Fatal("register user error:", err)
	}

	if err := db.AutoMigrate(&models.ImageFace{}, &models.FaceGroup{}, &models.Media{}, &models.Album{}); err != nil {
		t.Fatal("automigrate error:", err)
	}

	r := &mutationResolver{
		Resolver: &Resolver{
			database: db,
		},
	}

	return r, auth.AddUserToContext(context.Background(), user), user
}

func createFaceMutationFixtures(t *testing.T, r *mutationResolver) {
	t.Helper()

	db := r.database

	testAlbum := models.Album{Title: "Test Album", Path: "/test-album"}
	if err := db.Create(&testAlbum).Error; err != nil {
		t.Fatal(err)
	}

	testMedia := []models.Media{
		{Model: models.Model{ID: 1}, Path: "test1", AlbumID: testAlbum.ID},
		{Model: models.Model{ID: 2}, Path: "test2", AlbumID: testAlbum.ID},
		{Model: models.Model{ID: 3}, Path: "test3", AlbumID: testAlbum.ID},
		{Model: models.Model{ID: 4}, Path: "test4", AlbumID: testAlbum.ID},
	}
	if err := db.Create(&testMedia).Error; err != nil {
		t.Fatal(err)
	}

	testFaceGroups := []models.FaceGroup{
		{Model: models.Model{ID: 1}},
		{Model: models.Model{ID: 2}},
		{Model: models.Model{ID: 3}},
		{Model: models.Model{ID: 4}},
	}
	if err := db.Create(&testFaceGroups).Error; err != nil {
		t.Fatal(err)
	}

	// Postgres does not move a sequence past IDs that were inserted
	// explicitly, so the next face group a test creates would get ID 1 again.
	if drivers.POSTGRES.MatchDatabase(db) {
		if err := db.Exec("SELECT setval(pg_get_serial_sequence('face_groups', 'id'), (SELECT MAX(id) FROM face_groups))").Error; err != nil {
			t.Fatal(err)
		}
	}
}

func setupNonAdminUserWithAlbum(t *testing.T, r *mutationResolver) (*models.User, context.Context) {
	t.Helper()
	db := r.database

	pass := "5678"
	user, err := models.RegisterUser(db, "test_nonadmin", &pass, false)
	if err != nil {
		t.Fatal("register non-admin user error:", err)
	}

	var album models.Album
	if err := db.Where("path = ?", "/test-album").First(&album).Error; err != nil {
		t.Fatal("find fixture album error:", err)
	}

	if err := db.Model(user).Association("Albums").Append(&album); err != nil {
		t.Fatal("link non-admin user to album error:", err)
	}

	return user, auth.AddUserToContext(context.Background(), user)
}

// recordingFaceDetector wraps the real detector and records how a resolver
// asks it to regroup faces, since its own state is not visible from here.
type recordingFaceDetector struct {
	face_detection.FaceDetector
	mergedCategories [][2]int32
	mergedImageFaces map[int32][]int
}

func (d *recordingFaceDetector) MergeCategories(sourceID int32, destID int32) {
	d.mergedCategories = append(d.mergedCategories, [2]int32{sourceID, destID})
	d.FaceDetector.MergeCategories(sourceID, destID)
}

func (d *recordingFaceDetector) MergeImageFaces(imageFaceIDs []int, destFaceGroupID int32) {
	if d.mergedImageFaces == nil {
		d.mergedImageFaces = map[int32][]int{}
	}
	d.mergedImageFaces[destFaceGroupID] = append(d.mergedImageFaces[destFaceGroupID], imageFaceIDs...)
	d.FaceDetector.MergeImageFaces(imageFaceIDs, destFaceGroupID)
}

func recordFaceDetector(t *testing.T) *recordingFaceDetector {
	t.Helper()

	recorder := &recordingFaceDetector{FaceDetector: face_detection.GlobalFaceDetector}
	face_detection.GlobalFaceDetector = recorder
	t.Cleanup(func() { face_detection.GlobalFaceDetector = recorder.FaceDetector })

	return recorder
}

func faceGroupOf(t *testing.T, r *mutationResolver, imageFaceID int) int {
	t.Helper()

	var face models.ImageFace
	if err := r.database.First(&face, imageFaceID).Error; err != nil {
		t.Fatalf("image face %d: %v", imageFaceID, err)
	}

	return face.FaceGroupID
}

func assertFaceGroupMembers(t *testing.T, r *mutationResolver, faceGroupID int, want []int) {
	t.Helper()

	var got []int
	if err := r.database.Model(&models.ImageFace{}).
		Where("face_group_id = ?", faceGroupID).
		Order("id").
		Pluck("id", &got).Error; err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("expected face group %d to hold faces %v, got %v", faceGroupID, want, got)
	}
}

func assertFaceGroupDeleted(t *testing.T, r *mutationResolver, faceGroupID int) {
	t.Helper()

	var count int64
	if err := r.database.Model(&models.FaceGroup{}).Where("id = ?", faceGroupID).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("expected face group %d to be deleted", faceGroupID)
	}
}

func TestCombineFaceGroups(t *testing.T) {
	t.Run("merges face groups when media does not overlap", func(t *testing.T) {
		r, ctx, _ := setupFaceMutationTest(t)
		createFaceMutationFixtures(t, r)

		testDataList := []models.ImageFace{
			{Model: models.Model{ID: 1}, FaceGroupID: 1, MediaID: 1},
			{Model: models.Model{ID: 2}, FaceGroupID: 1, MediaID: 2},
			{Model: models.Model{ID: 3}, FaceGroupID: 2, MediaID: 3},
			{Model: models.Model{ID: 4}, FaceGroupID: 3, MediaID: 4},
		}
		if err := r.database.Create(&testDataList).Error; err != nil {
			t.Fatal(err)
		}

		result, err := r.CombineFaceGroups(ctx, 1, []int{2, 3})
		if err != nil {
			t.Fatal("CombineFaceGroups failed:", err)
		}
		if result == nil || result.ID != 1 {
			t.Fatalf("expected destination face group 1, got %#v", result)
		}

		var imageFaces []*models.ImageFace
		if err := r.database.Where("face_group_id = ?", 1).Find(&imageFaces).Error; err != nil {
			t.Fatal("failed to query destination image faces:", err)
		}

		if len(imageFaces) != 4 {
			t.Fatalf("expected 4 image faces after merge, got %d", len(imageFaces))
		}
	})

	t.Run("detaches a source face on a photo the destination already shows", func(t *testing.T) {
		r, ctx, _ := setupFaceMutationTest(t)
		createFaceMutationFixtures(t, r)
		recorder := recordFaceDetector(t)

		testDataList := []models.ImageFace{
			{Model: models.Model{ID: 1}, FaceGroupID: 1, MediaID: 1},
			{Model: models.Model{ID: 2}, FaceGroupID: 1, MediaID: 2},
			{Model: models.Model{ID: 3}, FaceGroupID: 2, MediaID: 2},
			{Model: models.Model{ID: 4}, FaceGroupID: 2, MediaID: 3},
		}
		if err := r.database.Create(&testDataList).Error; err != nil {
			t.Fatal(err)
		}

		result, err := r.CombineFaceGroups(ctx, 1, []int{2})
		if err != nil {
			t.Fatal("CombineFaceGroups failed:", err)
		}
		if result == nil || result.ID != 1 {
			t.Fatalf("expected destination face group 1, got %#v", result)
		}

		// The destination keeps its own face on photo 2; the source's face
		// there is someone else and gets a group of its own, not deleted.
		assertFaceGroupMembers(t, r, 1, []int{1, 2, 4})
		detachedGroup := faceGroupOf(t, r, 3)
		if detachedGroup == 1 || detachedGroup == 2 {
			t.Fatalf("expected face 3 in a new group, got group %d", detachedGroup)
		}
		assertFaceGroupMembers(t, r, detachedGroup, []int{3})
		assertFaceGroupDeleted(t, r, 2)

		if fmt.Sprint(recorder.mergedImageFaces[int32(detachedGroup)]) != "[3]" {
			t.Fatalf("expected the recognizer to move face 3 to group %d, got %v", detachedGroup, recorder.mergedImageFaces)
		}
	})

	t.Run("keeps one face per photo across several sources", func(t *testing.T) {
		r, ctx, _ := setupFaceMutationTest(t)
		createFaceMutationFixtures(t, r)

		testDataList := []models.ImageFace{
			{Model: models.Model{ID: 1}, FaceGroupID: 1, MediaID: 1},
			{Model: models.Model{ID: 2}, FaceGroupID: 2, MediaID: 2},
			{Model: models.Model{ID: 3}, FaceGroupID: 3, MediaID: 2},
		}
		if err := r.database.Create(&testDataList).Error; err != nil {
			t.Fatal(err)
		}

		if _, err := r.CombineFaceGroups(ctx, 1, []int{2, 3}); err != nil {
			t.Fatal("CombineFaceGroups failed:", err)
		}

		// Neither face on photo 2 was in the destination, so the one detected
		// first comes along.
		assertFaceGroupMembers(t, r, 1, []int{1, 2})
		assertFaceGroupMembers(t, r, faceGroupOf(t, r, 3), []int{3})
		assertFaceGroupDeleted(t, r, 2)
		assertFaceGroupDeleted(t, r, 3)
	})

	t.Run("gives every surplus face on a group photo its own group", func(t *testing.T) {
		r, ctx, _ := setupFaceMutationTest(t)
		createFaceMutationFixtures(t, r)

		// The case that prompted this: the smaller group holds three faces of
		// one group photo, so at least two of them are other people.
		testDataList := []models.ImageFace{
			{Model: models.Model{ID: 1}, FaceGroupID: 1, MediaID: 1},
			{Model: models.Model{ID: 2}, FaceGroupID: 2, MediaID: 2},
			{Model: models.Model{ID: 3}, FaceGroupID: 2, MediaID: 2},
			{Model: models.Model{ID: 4}, FaceGroupID: 2, MediaID: 2},
			{Model: models.Model{ID: 5}, FaceGroupID: 2, MediaID: 3},
		}
		if err := r.database.Create(&testDataList).Error; err != nil {
			t.Fatal(err)
		}

		if _, err := r.CombineFaceGroups(ctx, 1, []int{2}); err != nil {
			t.Fatal("CombineFaceGroups failed:", err)
		}

		assertFaceGroupMembers(t, r, 1, []int{1, 2, 5})
		// Separate groups: the two may well be two different people.
		group3, group4 := faceGroupOf(t, r, 3), faceGroupOf(t, r, 4)
		if group3 == group4 {
			t.Fatalf("expected faces 3 and 4 in separate groups, both are in %d", group3)
		}
		assertFaceGroupMembers(t, r, group3, []int{3})
		assertFaceGroupMembers(t, r, group4, []int{4})

		var total int64
		if err := r.database.Model(&models.ImageFace{}).Count(&total).Error; err != nil {
			t.Fatal(err)
		}
		if total != 5 {
			t.Fatalf("expected no face to be deleted, %d of 5 left", total)
		}
	})

	t.Run("non-admin user merges owned face groups", func(t *testing.T) {
		r, _, _ := setupFaceMutationTest(t)
		createFaceMutationFixtures(t, r)
		_, ctx := setupNonAdminUserWithAlbum(t, r)

		testDataList := []models.ImageFace{
			{Model: models.Model{ID: 1}, FaceGroupID: 1, MediaID: 1},
			{Model: models.Model{ID: 2}, FaceGroupID: 1, MediaID: 2},
			{Model: models.Model{ID: 3}, FaceGroupID: 2, MediaID: 3},
			{Model: models.Model{ID: 4}, FaceGroupID: 3, MediaID: 4},
		}
		if err := r.database.Create(&testDataList).Error; err != nil {
			t.Fatal(err)
		}

		result, err := r.CombineFaceGroups(ctx, 1, []int{2, 3})
		if err != nil {
			t.Fatal("CombineFaceGroups failed for non-admin user:", err)
		}
		if result == nil || result.ID != 1 {
			t.Fatalf("expected destination face group 1, got %#v", result)
		}

		var imageFaces []*models.ImageFace
		if err := r.database.Where("face_group_id = ?", 1).Find(&imageFaces).Error; err != nil {
			t.Fatal("failed to query destination image faces:", err)
		}
		if len(imageFaces) != 4 {
			t.Fatalf("expected 4 image faces after non-admin merge, got %d", len(imageFaces))
		}
	})

	t.Run("moves the merged groups over in the recognizer too", func(t *testing.T) {
		r, ctx, _ := setupFaceMutationTest(t)
		createFaceMutationFixtures(t, r)
		recorder := recordFaceDetector(t)

		testDataList := []models.ImageFace{
			{Model: models.Model{ID: 1}, FaceGroupID: 1, MediaID: 1},
			{Model: models.Model{ID: 2}, FaceGroupID: 2, MediaID: 2},
			{Model: models.Model{ID: 3}, FaceGroupID: 3, MediaID: 3},
		}
		if err := r.database.Create(&testDataList).Error; err != nil {
			t.Fatal(err)
		}

		if _, err := r.CombineFaceGroups(ctx, 1, []int{2, 3}); err != nil {
			t.Fatal("CombineFaceGroups failed:", err)
		}

		// The source IDs are face group IDs. Handing them to MergeImageFaces,
		// which takes image face IDs, would regroup whichever faces happen to
		// have those IDs and leave the merged ones on the deleted groups.
		want := [][2]int32{{2, 1}, {3, 1}}
		if fmt.Sprint(recorder.mergedCategories) != fmt.Sprint(want) {
			t.Fatalf("expected the recognizer to merge groups %v, got %v", want, recorder.mergedCategories)
		}
		if len(recorder.mergedImageFaces) != 0 {
			t.Fatalf("expected no image faces to be regrouped one by one, got %v", recorder.mergedImageFaces)
		}
	})
}

func TestMoveImageFaces(t *testing.T) {
	t.Run("moves image faces when destination does not contain selected media", func(t *testing.T) {
		r, ctx, _ := setupFaceMutationTest(t)
		createFaceMutationFixtures(t, r)

		testDataList := []models.ImageFace{
			{Model: models.Model{ID: 1}, FaceGroupID: 1, MediaID: 1},
			{Model: models.Model{ID: 2}, FaceGroupID: 2, MediaID: 2},
		}
		if err := r.database.Create(&testDataList).Error; err != nil {
			t.Fatal(err)
		}

		result, err := r.MoveImageFaces(ctx, []int{1}, 2)
		if err != nil {
			t.Fatal("MoveImageFaces failed:", err)
		}
		if result == nil || result.ID != 2 {
			t.Fatalf("expected destination face group 2, got %#v", result)
		}

		var movedFace models.ImageFace
		if err := r.database.First(&movedFace, 1).Error; err != nil {
			t.Fatal(err)
		}
		if movedFace.FaceGroupID != 2 {
			t.Fatalf("expected image face to move to group 2, got group %d", movedFace.FaceGroupID)
		}
	})

	t.Run("rejects move when destination already contains selected media", func(t *testing.T) {
		r, ctx, _ := setupFaceMutationTest(t)
		createFaceMutationFixtures(t, r)

		testDataList := []models.ImageFace{
			{Model: models.Model{ID: 1}, FaceGroupID: 1, MediaID: 1},
			{Model: models.Model{ID: 2}, FaceGroupID: 2, MediaID: 1},
		}
		if err := r.database.Create(&testDataList).Error; err != nil {
			t.Fatal(err)
		}

		result, err := r.MoveImageFaces(ctx, []int{1}, 2)
		if err == nil {
			t.Fatal("expected MoveImageFaces to reject duplicate media, got nil error")
		}
		if result != nil {
			t.Fatalf("expected nil result when move is rejected, got %#v", result)
		}
		if !strings.Contains(err.Error(), "duplicate images") {
			t.Fatalf("expected duplicate image validation error, got: %v", err)
		}

		var originalFace models.ImageFace
		if err := r.database.First(&originalFace, 1).Error; err != nil {
			t.Fatal(err)
		}
		if originalFace.FaceGroupID != 1 {
			t.Fatalf("expected rejected move to leave image face in group 1, got group %d", originalFace.FaceGroupID)
		}
	})

	t.Run("rejects move when selected faces contain duplicate media", func(t *testing.T) {
		r, ctx, _ := setupFaceMutationTest(t)
		createFaceMutationFixtures(t, r)

		testDataList := []models.ImageFace{
			{Model: models.Model{ID: 1}, FaceGroupID: 1, MediaID: 1},
			{Model: models.Model{ID: 2}, FaceGroupID: 3, MediaID: 1},
			{Model: models.Model{ID: 3}, FaceGroupID: 2, MediaID: 2},
		}
		if err := r.database.Create(&testDataList).Error; err != nil {
			t.Fatal(err)
		}

		result, err := r.MoveImageFaces(ctx, []int{1, 2}, 2)
		if err == nil {
			t.Fatal("expected MoveImageFaces to reject duplicate selected media, got nil error")
		}
		if result != nil {
			t.Fatalf("expected nil result when move is rejected, got %#v", result)
		}
		if !strings.Contains(err.Error(), "duplicate images") {
			t.Fatalf("expected duplicate image validation error, got: %v", err)
		}
	})

	t.Run("non-admin user moves owned image faces", func(t *testing.T) {
		r, _, _ := setupFaceMutationTest(t)
		createFaceMutationFixtures(t, r)
		_, ctx := setupNonAdminUserWithAlbum(t, r)

		testDataList := []models.ImageFace{
			{Model: models.Model{ID: 1}, FaceGroupID: 1, MediaID: 1},
			{Model: models.Model{ID: 2}, FaceGroupID: 2, MediaID: 2},
		}
		if err := r.database.Create(&testDataList).Error; err != nil {
			t.Fatal(err)
		}

		result, err := r.MoveImageFaces(ctx, []int{1}, 2)
		if err != nil {
			t.Fatal("MoveImageFaces failed for non-admin user:", err)
		}
		if result == nil || result.ID != 2 {
			t.Fatalf("expected destination face group 2, got %#v", result)
		}

		var movedFace models.ImageFace
		if err := r.database.First(&movedFace, 1).Error; err != nil {
			t.Fatal(err)
		}
		if movedFace.FaceGroupID != 2 {
			t.Fatalf("expected image face to move to group 2, got group %d", movedFace.FaceGroupID)
		}
	})
}

func TestGetUserOwnedImageFaces(t *testing.T) {
	t.Run("returns empty when image face IDs list is empty", func(t *testing.T) {
		r, _, user := setupFaceMutationTest(t)

		faces, err := getUserOwnedImageFaces(r.database, user, []int{})
		if err != nil {
			t.Fatal("unexpected error:", err)
		}
		if len(faces) != 0 {
			t.Fatalf("expected 0 faces for empty input, got %d", len(faces))
		}
	})

	t.Run("non-admin user with no album associations returns empty", func(t *testing.T) {
		r, _, _ := setupFaceMutationTest(t)
		createFaceMutationFixtures(t, r)

		testDataList := []models.ImageFace{
			{Model: models.Model{ID: 1}, FaceGroupID: 1, MediaID: 1},
		}
		if err := r.database.Create(&testDataList).Error; err != nil {
			t.Fatal(err)
		}

		pass := "5678"
		userNoAlbums, err := models.RegisterUser(r.database, "test_noalbums", &pass, false)
		if err != nil {
			t.Fatal("register user error:", err)
		}

		faces, err := getUserOwnedImageFaces(r.database, userNoAlbums, []int{1})
		if err != nil {
			t.Fatal("unexpected error:", err)
		}
		if len(faces) != 0 {
			t.Fatalf("expected 0 faces for user with no albums, got %d", len(faces))
		}
	})
}
