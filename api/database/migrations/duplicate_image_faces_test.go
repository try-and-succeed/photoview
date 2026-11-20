package migrations_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/photoview/photoview/api/database/migrations"
	"github.com/photoview/photoview/api/graphql/models"
	"github.com/photoview/photoview/api/test_utils"
)

func TestDuplicateImageFacesMigration(t *testing.T) {
	db := test_utils.DatabaseTest(t)
	t.Cleanup(func() {
		db.Exec("DELETE FROM image_faces")
		db.Exec("DELETE FROM face_groups")
		db.Exec("DELETE FROM media")
		db.Exec("DELETE FROM albums")
	})

	album := models.Album{Title: "album", Path: "/album"}
	require.NoError(t, db.Create(&album).Error)

	media := []models.Media{
		{Title: "one", Path: "/album/one.jpg", AlbumID: album.ID},
		{Title: "two", Path: "/album/two.jpg", AlbumID: album.ID},
		{Title: "three", Path: "/album/three.jpg", AlbumID: album.ID},
	}
	require.NoError(t, db.Create(&media).Error)

	label := func(s string) *string { return &s }
	groups := map[string]*models.FaceGroup{
		"unnamed":      {},
		"anna":         {Label: label("Anna")},
		"only copies":  {},
		"ben":          {Label: label("Ben")},
		"carl":         {Label: label("Carl")},
		"empty before": {},
	}
	for _, name := range []string{"unnamed", "anna", "only copies", "ben", "carl", "empty before"} {
		require.NoError(t, db.Create(groups[name]).Error)
	}

	detected := time.Now().Add(-48 * time.Hour).Truncate(time.Second)
	face := func(group string, mediaIndex int, rect models.FaceRectangle) *models.ImageFace {
		return &models.ImageFace{
			Model:       models.Model{ModelTimestamps: models.ModelTimestamps{CreatedAt: detected, UpdatedAt: detected}},
			FaceGroupID: groups[group].ID,
			MediaID:     media[mediaIndex].ID,
			Rectangle:   rect,
		}
	}
	rectA := models.FaceRectangle{MinX: 0.1, MaxX: 0.2, MinY: 0.1, MaxY: 0.2}
	rectB := models.FaceRectangle{MinX: 0.3, MaxX: 0.4, MinY: 0.3, MaxY: 0.4}
	rectC := models.FaceRectangle{MinX: 0.5, MaxX: 0.6, MinY: 0.5, MaxY: 0.6}
	rectD := models.FaceRectangle{MinX: 0.7, MaxX: 0.8, MinY: 0.7, MaxY: 0.8}

	// Three copies of one face in the same group: the oldest stays.
	copyA1, copyA2, copyA3 := face("unnamed", 0, rectA), face("unnamed", 0, rectA), face("unnamed", 0, rectA)
	// Two different faces of one person on one photo are no copies.
	secondFaceOnPhoto := face("unnamed", 0, rectD)
	// A copy in an unnamed group and one in a named group: the named stays,
	// and the unnamed group, holding nothing else, goes.
	copyBUnnamed, copyBNamed := face("only copies", 1, rectB), face("anna", 1, rectB)
	// Copies in two named groups: the one the user moved last stays.
	copyCBen, copyCCarl := face("ben", 2, rectC), face("carl", 2, rectC)
	copyCCarl.UpdatedAt = detected.Add(time.Hour)
	bensOtherFace := face("ben", 0, rectB)

	all := []*models.ImageFace{copyA1, copyA2, copyA3, secondFaceOnPhoto, copyBUnnamed, copyBNamed, copyCBen, copyCCarl, bensOtherFace}
	for _, f := range all {
		require.NoError(t, db.Create(f).Error)
	}

	remaining := func() []int {
		var ids []int
		require.NoError(t, db.Model(&models.ImageFace{}).Order("id").Pluck("id", &ids).Error)
		return ids
	}
	groupExists := func(name string) bool {
		var count int64
		require.NoError(t, db.Model(&models.FaceGroup{}).Where("id = ?", groups[name].ID).Count(&count).Error)
		return count == 1
	}

	require.NoError(t, migrations.MigrateForDuplicateImageFaces(db))

	want := []int{copyA1.ID, secondFaceOnPhoto.ID, copyBNamed.ID, copyCCarl.ID, bensOtherFace.ID}
	assert.Equal(t, want, remaining())
	assert.False(t, groupExists("only copies"), "a group that held only copies should be deleted")
	for _, name := range []string{"unnamed", "anna", "ben", "carl", "empty before"} {
		assert.True(t, groupExists(name), "group %q should be kept", name)
	}

	require.NoError(t, migrations.MigrateForDuplicateImageFaces(db), "second run")
	assert.Equal(t, want, remaining(), "a second run should change nothing")
}
