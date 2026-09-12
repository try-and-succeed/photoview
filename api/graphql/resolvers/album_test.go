package resolvers

import (
	"context"
	"testing"

	"github.com/photoview/photoview/api/graphql/auth"
	"github.com/photoview/photoview/api/graphql/models"
	"github.com/photoview/photoview/api/test_utils"
	"github.com/stretchr/testify/assert"
)

func TestAlbumTreeChildren(t *testing.T) {
	db := test_utils.DatabaseTest(t)

	ownedRoot := models.Album{Title: "owned_root", Path: "/photos/owned"}
	assert.NoError(t, db.Save(&ownedRoot).Error)
	ownedChild := models.Album{Title: "b_child", Path: "/photos/owned/b", ParentAlbumID: &ownedRoot.ID}
	assert.NoError(t, db.Save(&ownedChild).Error)
	ownedChild2 := models.Album{Title: "a_child", Path: "/photos/owned/a", ParentAlbumID: &ownedRoot.ID}
	assert.NoError(t, db.Save(&ownedChild2).Error)

	foreignRoot := models.Album{Title: "foreign_root", Path: "/photos/foreign"}
	assert.NoError(t, db.Save(&foreignRoot).Error)
	foreignChild := models.Album{Title: "secret", Path: "/photos/foreign/secret", ParentAlbumID: &foreignRoot.ID}
	assert.NoError(t, db.Save(&foreignChild).Error)

	user, err := models.RegisterUser(db, "tree_user", nil, false)
	assert.NoError(t, err)
	assert.NoError(t, db.Model(&user).Association("Albums").Append(&ownedRoot, &ownedChild, &ownedChild2))

	admin, err := models.RegisterUser(db, "tree_admin", nil, true)
	assert.NoError(t, err)

	r := &queryResolver{Resolver: &Resolver{database: db}}

	t.Run("children come back grouped by parent and sorted by title", func(t *testing.T) {
		result, err := r.AlbumTreeChildren(auth.AddUserToContext(context.Background(), user), []int{ownedRoot.ID})
		assert.NoError(t, err)

		if assert.Len(t, result, 1) {
			assert.Equal(t, ownedRoot.ID, result[0].AlbumID)
			if assert.Len(t, result[0].Children, 2) {
				assert.Equal(t, "a_child", result[0].Children[0].Title)
				assert.Equal(t, "b_child", result[0].Children[1].Title)
			}
		}
	})

	t.Run("an album id the caller has no access to yields nothing", func(t *testing.T) {
		// The ids come straight from the client here, unlike the subAlbums
		// field, so this is the check that stops someone reading another
		// user's folder names by guessing ids.
		result, err := r.AlbumTreeChildren(auth.AddUserToContext(context.Background(), user), []int{foreignRoot.ID})
		assert.NoError(t, err)

		if assert.Len(t, result, 1) {
			assert.Equal(t, foreignRoot.ID, result[0].AlbumID)
			assert.Empty(t, result[0].Children)
		}
	})

	t.Run("an admin sees children of any album", func(t *testing.T) {
		result, err := r.AlbumTreeChildren(auth.AddUserToContext(context.Background(), admin), []int{foreignRoot.ID})
		assert.NoError(t, err)

		if assert.Len(t, result, 1) {
			assert.Len(t, result[0].Children, 1)
		}
	})

	t.Run("an empty request is answered without touching the database", func(t *testing.T) {
		result, err := r.AlbumTreeChildren(auth.AddUserToContext(context.Background(), user), []int{})
		assert.NoError(t, err)
		assert.Empty(t, result)
	})

	t.Run("an unauthenticated request is refused", func(t *testing.T) {
		_, err := r.AlbumTreeChildren(context.Background(), []int{ownedRoot.ID})
		assert.Error(t, err)
	})
}
