package resolvers

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/photoview/photoview/api/graphql/auth"
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

func faceIDs(faces []*models.ImageFace) string {
	ids := make([]int, len(faces))
	for i, face := range faces {
		ids[i] = face.ID
	}
	return fmt.Sprint(ids)
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

func TestSetFacesVerified(t *testing.T) {
	t.Run("confirming single faces clears their review flag", func(t *testing.T) {
		r, ctx, _ := setupVerifyTest(t)
		setFaceFlags(t, r, []int{1, 2}, map[string]any{"needs_review": true})

		changed, err := r.SetImageFacesVerified(ctx, []int{1}, true)
		if err != nil {
			t.Fatal("SetImageFacesVerified error:", err)
		}
		if got := faceIDs(changed); got != "[1]" {
			t.Errorf("changed faces = %s, want [1]", got)
		}

		if face := loadFace(t, r, 1); !face.Verified || face.NeedsReview {
			t.Errorf("face 1 = verified %v, review %v; want true, false", face.Verified, face.NeedsReview)
		}
		if face := loadFace(t, r, 2); face.Verified || !face.NeedsReview {
			t.Errorf("face 2 = verified %v, review %v; want it untouched", face.Verified, face.NeedsReview)
		}
	})

	t.Run("taking a confirmation back keeps the review flag", func(t *testing.T) {
		r, ctx, _ := setupVerifyTest(t)
		setFaceFlags(t, r, []int{1}, map[string]any{"verified": true, "needs_review": true})

		if _, err := r.SetImageFacesVerified(ctx, []int{1}, false); err != nil {
			t.Fatal("SetImageFacesVerified error:", err)
		}

		if face := loadFace(t, r, 1); face.Verified || !face.NeedsReview {
			t.Errorf("face 1 = verified %v, review %v; want false, true", face.Verified, face.NeedsReview)
		}
	})

	t.Run("confirming a photo confirms every face on it", func(t *testing.T) {
		r, ctx, _ := setupVerifyTest(t)

		changed, err := r.SetMediaFacesVerified(ctx, []int{2}, true)
		if err != nil {
			t.Fatal("SetMediaFacesVerified error:", err)
		}
		if got := faceIDs(changed); got != "[2 3]" {
			t.Errorf("changed faces = %s, want [2 3]", got)
		}
		if face := loadFace(t, r, 1); face.Verified {
			t.Error("face 1 is on another photo and should stay unconfirmed")
		}
	})

	t.Run("empty lists change nothing", func(t *testing.T) {
		r, ctx, _ := setupVerifyTest(t)

		for name, call := range map[string]func() ([]*models.ImageFace, error){
			"faces":  func() ([]*models.ImageFace, error) { return r.SetImageFacesVerified(ctx, []int{}, true) },
			"photos": func() ([]*models.ImageFace, error) { return r.SetMediaFacesVerified(ctx, []int{}, true) },
		} {
			changed, err := call()
			if err != nil {
				t.Fatalf("%s: unexpected error %v", name, err)
			}
			if changed == nil || len(changed) != 0 {
				t.Errorf("%s: changed = %v, want an empty list", name, changed)
			}
		}

		var verified int64
		if err := r.database.Model(&models.ImageFace{}).Where("verified = ?", true).Count(&verified).Error; err != nil {
			t.Fatal(err)
		}
		if verified != 0 {
			t.Errorf("%d faces confirmed, want none", verified)
		}
	})

	t.Run("faces outside the user's albums are skipped", func(t *testing.T) {
		r, _, _ := setupVerifyTest(t)
		_, userCtx := setupNonAdminUserWithAlbum(t, r)

		otherAlbum := models.Album{Title: "other", Path: "/other-album"}
		if err := r.database.Create(&otherAlbum).Error; err != nil {
			t.Fatal(err)
		}
		otherMedia := models.Media{Model: models.Model{ID: 5}, Path: "other", AlbumID: otherAlbum.ID}
		if err := r.database.Create(&otherMedia).Error; err != nil {
			t.Fatal(err)
		}
		if err := r.database.Create(&models.ImageFace{Model: models.Model{ID: 5}, FaceGroupID: 4, MediaID: 5}).Error; err != nil {
			t.Fatal(err)
		}

		changed, err := r.SetImageFacesVerified(userCtx, []int{1, 5}, true)
		if err != nil {
			t.Fatal("SetImageFacesVerified error:", err)
		}
		if got := faceIDs(changed); got != "[1]" {
			t.Errorf("changed faces = %s, want [1]", got)
		}
		if face := loadFace(t, r, 5); face.Verified {
			t.Error("face 5 is outside the user's albums and should stay unconfirmed")
		}

		changed, err = r.SetMediaFacesVerified(userCtx, []int{5}, true)
		if err != nil {
			t.Fatal("SetMediaFacesVerified error:", err)
		}
		if len(changed) != 0 {
			t.Errorf("changed faces = %s, want none", faceIDs(changed))
		}
	})
}

func TestFaceGroupVerificationFields(t *testing.T) {
	r, ctx, _ := setupVerifyTest(t)
	// Group 1: face 1 confirmed, face 2 up for review.
	setFaceFlags(t, r, []int{1}, map[string]any{"verified": true})
	setFaceFlags(t, r, []int{2}, map[string]any{"needs_review": true})

	groupResolver := &faceGroupResolver{r.Resolver}
	group := &models.FaceGroup{Model: models.Model{ID: 1}}

	yes, no := true, false
	for _, tc := range []struct {
		name   string
		filter *models.ImageFaceFilter
		want   string
	}{
		{"no filter", nil, "[1 2]"},
		{"verified", &models.ImageFaceFilter{Verified: &yes}, "[1]"},
		{"not verified", &models.ImageFaceFilter{Verified: &no}, "[2]"},
		{"needs review", &models.ImageFaceFilter{NeedsReview: &yes}, "[2]"},
		{"both", &models.ImageFaceFilter{Verified: &yes, NeedsReview: &yes}, "[]"},
	} {
		t.Run("imageFaces "+tc.name, func(t *testing.T) {
			faces, err := groupResolver.ImageFaces(ctx, group, nil, tc.filter)
			if err != nil {
				t.Fatal("ImageFaces error:", err)
			}
			if got := faceIDs(faces); got != tc.want {
				t.Errorf("faces = %s, want %s", got, tc.want)
			}
		})
	}

	t.Run("counts", func(t *testing.T) {
		verified, err := groupResolver.VerifiedFaceCount(ctx, group)
		if err != nil {
			t.Fatal(err)
		}
		review, err := groupResolver.ReviewFaceCount(ctx, group)
		if err != nil {
			t.Fatal(err)
		}
		if verified != 1 || review != 1 {
			t.Errorf("verifiedFaceCount = %d, reviewFaceCount = %d; want 1 and 1", verified, review)
		}
	})
}

func TestSetFacesNeedsReview(t *testing.T) {
	reviewList := func(t *testing.T, r *mutationResolver, ctx context.Context) string {
		t.Helper()
		media, err := (&queryResolver{r.Resolver}).MyMediaNeedingReview(ctx, nil, nil)
		if err != nil {
			t.Fatal("MyMediaNeedingReview error:", err)
		}
		ids := make([]int, len(media))
		for i, m := range media {
			ids[i] = m.ID
		}
		return fmt.Sprint(ids)
	}

	t.Run("flagging touches only the flag of the given faces", func(t *testing.T) {
		r, ctx, _ := setupVerifyTest(t)

		changed, err := r.SetImageFacesNeedsReview(ctx, []int{1, 3}, true)
		if err != nil {
			t.Fatal("SetImageFacesNeedsReview error:", err)
		}
		if got := faceIDs(changed); got != "[1 3]" {
			t.Errorf("changed faces = %s, want [1 3]", got)
		}
		for _, face := range changed {
			if !face.NeedsReview {
				t.Errorf("returned face %d is not flagged", face.ID)
			}
		}

		for id, group := range map[int]int{1: 1, 3: 2} {
			face := loadFace(t, r, id)
			if !face.NeedsReview || face.Verified || face.FaceGroupID != group || face.AssignedBy != models.FaceAssignmentDetector {
				t.Errorf("face %d = review %v, verified %v, group %d, %s; want flagged, unconfirmed, group %d, DETECTOR",
					id, face.NeedsReview, face.Verified, face.FaceGroupID, face.AssignedBy, group)
			}
		}
		for _, id := range []int{2, 4} {
			if face := loadFace(t, r, id); face.NeedsReview {
				t.Errorf("face %d should stay unflagged", id)
			}
		}

		// Face 3 is on photo 2, so photos 1 and 2 are up for review.
		if got := reviewList(t, r, ctx); got != "[2 1]" && got != "[1 2]" {
			t.Errorf("photos up for review = %s, want 1 and 2", got)
		}
	})

	t.Run("flagging a confirmed face keeps it confirmed", func(t *testing.T) {
		r, ctx, _ := setupVerifyTest(t)
		setFaceFlags(t, r, []int{1}, map[string]any{"verified": true})

		if _, err := r.SetImageFacesNeedsReview(ctx, []int{1}, true); err != nil {
			t.Fatal("SetImageFacesNeedsReview error:", err)
		}

		if face := loadFace(t, r, 1); !face.NeedsReview || !face.Verified {
			t.Errorf("face 1 = review %v, verified %v; want true, true", face.NeedsReview, face.Verified)
		}
	})

	t.Run("clearing the flag does not confirm", func(t *testing.T) {
		r, ctx, _ := setupVerifyTest(t)
		setFaceFlags(t, r, []int{1, 3}, map[string]any{"needs_review": true})

		changed, err := r.SetImageFacesNeedsReview(ctx, []int{1, 3}, false)
		if err != nil {
			t.Fatal("SetImageFacesNeedsReview error:", err)
		}
		if got := faceIDs(changed); got != "[1 3]" {
			t.Errorf("changed faces = %s, want [1 3]", got)
		}

		for _, id := range []int{1, 3} {
			if face := loadFace(t, r, id); face.NeedsReview || face.Verified {
				t.Errorf("face %d = review %v, verified %v; want false, false", id, face.NeedsReview, face.Verified)
			}
		}
		if got := reviewList(t, r, ctx); got != "[]" {
			t.Errorf("photos up for review = %s, want none", got)
		}
	})

	t.Run("faces outside the user's albums are skipped", func(t *testing.T) {
		r, _, _ := setupVerifyTest(t)
		_, userCtx := setupNonAdminUserWithAlbum(t, r)

		otherAlbum := models.Album{Title: "other", Path: "/other-album"}
		if err := r.database.Create(&otherAlbum).Error; err != nil {
			t.Fatal(err)
		}
		if err := r.database.Create(&models.Media{Model: models.Model{ID: 5}, Path: "other", AlbumID: otherAlbum.ID}).Error; err != nil {
			t.Fatal(err)
		}
		if err := r.database.Create(&models.ImageFace{Model: models.Model{ID: 5}, FaceGroupID: 4, MediaID: 5}).Error; err != nil {
			t.Fatal(err)
		}

		changed, err := r.SetImageFacesNeedsReview(userCtx, []int{1, 5}, true)
		if err != nil {
			t.Fatal("SetImageFacesNeedsReview error:", err)
		}
		if got := faceIDs(changed); got != "[1]" {
			t.Errorf("changed faces = %s, want [1]", got)
		}
		if face := loadFace(t, r, 5); face.NeedsReview {
			t.Error("face 5 is outside the user's albums and should stay unflagged")
		}
	})

	t.Run("an empty list changes nothing", func(t *testing.T) {
		r, ctx, _ := setupVerifyTest(t)

		changed, err := r.SetImageFacesNeedsReview(ctx, []int{}, true)
		if err != nil {
			t.Fatal("SetImageFacesNeedsReview error:", err)
		}
		if changed == nil || len(changed) != 0 {
			t.Errorf("changed = %v, want an empty list", changed)
		}

		var flagged int64
		if err := r.database.Model(&models.ImageFace{}).Where("needs_review = ?", true).Count(&flagged).Error; err != nil {
			t.Fatal(err)
		}
		if flagged != 0 {
			t.Errorf("%d faces flagged, want none", flagged)
		}
	})
}

func TestMyMediaNeedingReview(t *testing.T) {
	r, ctx, _ := setupVerifyTest(t)
	// Photos 1 and 2 share a date, so their id decides; photo 3 is newest.
	shot := time.Date(2024, 5, 1, 12, 0, 0, 0, time.UTC)
	for id, date := range map[int]time.Time{1: shot, 2: shot, 3: shot.Add(time.Hour), 4: shot.Add(2 * time.Hour)} {
		if err := r.database.Model(&models.Media{}).Where("id = ?", id).Update("date_shot", date).Error; err != nil {
			t.Fatal(err)
		}
	}
	// Faces 1 (photo 1, group 1), 3 (photo 2, group 2) and 4 (photo 3, group 3)
	// are up for review; photo 4 has no faces.
	setFaceFlags(t, r, []int{1, 3, 4}, map[string]any{"needs_review": true})

	queryRes := &queryResolver{r.Resolver}
	list := func(t *testing.T, group *int, paginate *models.Pagination) string {
		t.Helper()
		media, err := queryRes.MyMediaNeedingReview(ctx, group, paginate)
		if err != nil {
			t.Fatal("MyMediaNeedingReview error:", err)
		}
		ids := make([]int, len(media))
		for i, m := range media {
			ids[i] = m.ID
		}
		return fmt.Sprint(ids)
	}

	t.Run("newest first, then by id", func(t *testing.T) {
		if got := list(t, nil, nil); got != "[3 2 1]" {
			t.Errorf("photos = %s, want [3 2 1]", got)
		}
	})

	t.Run("only one group", func(t *testing.T) {
		group := 1
		if got := list(t, &group, nil); got != "[1]" {
			t.Errorf("photos of group 1 = %s, want [1]", got)
		}
	})

	t.Run("pages of one hold every photo once", func(t *testing.T) {
		got := ""
		for offset := 0; offset < 4; offset++ {
			limit, offset := 1, offset
			got += list(t, nil, &models.Pagination{Limit: &limit, Offset: &offset})
		}
		if got != "[3][2][1][]" {
			t.Errorf("pages = %s, want [3][2][1][]", got)
		}
	})
}

// The counts run once per face group, concurrently, and share the request's
// user: they must neither load its albums nor cache them on it.
func TestVerificationQueriesLeaveTheUserAlone(t *testing.T) {
	r, _, linked := setupVerifyTest(t)
	setFaceFlags(t, r, []int{1}, map[string]any{"verified": true})
	setFaceFlags(t, r, []int{2}, map[string]any{"needs_review": true})

	// A face on a photo outside the user's albums counts for nothing.
	other := models.Album{Title: "Other Album", Path: "/other-album"}
	if err := r.database.Create(&other).Error; err != nil {
		t.Fatal(err)
	}
	if err := r.database.Create(&models.Media{Model: models.Model{ID: 5}, Path: "test5", AlbumID: other.ID}).Error; err != nil {
		t.Fatal(err)
	}
	if err := r.database.Create(&models.ImageFace{Model: models.Model{ID: 5}, FaceGroupID: 1, MediaID: 5, Verified: true, NeedsReview: true}).Error; err != nil {
		t.Fatal(err)
	}

	// Linking the album filled linked.Albums; a request starts with a user
	// fresh from the database.
	user := &models.User{}
	if err := r.database.First(user, linked.ID).Error; err != nil {
		t.Fatal(err)
	}
	ctx := auth.AddUserToContext(context.Background(), user)

	groupResolver := &faceGroupResolver{r.Resolver}
	group := &models.FaceGroup{Model: models.Model{ID: 1}}
	verified, err := groupResolver.VerifiedFaceCount(ctx, group)
	if err != nil {
		t.Fatal(err)
	}
	review, err := groupResolver.ReviewFaceCount(ctx, group)
	if err != nil {
		t.Fatal(err)
	}
	if verified != 1 || review != 1 {
		t.Errorf("verifiedFaceCount = %d, reviewFaceCount = %d; want 1 and 1", verified, review)
	}

	media, err := (&queryResolver{r.Resolver}).MyMediaNeedingReview(ctx, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(media) != 1 || media[0].ID != 2 {
		t.Errorf("myMediaNeedingReview returned %d photos, want only photo 2", len(media))
	}

	if len(user.Albums) != 0 {
		t.Errorf("the queries loaded %d albums into the shared user", len(user.Albums))
	}
}
