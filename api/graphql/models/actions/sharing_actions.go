package actions

import (
	"github.com/photoview/photoview/api/graphql/models"
	"github.com/pkg/errors"
	"gorm.io/gorm"
)

// AlbumPermissions lists every user *other than viewerID* with an explicit
// access grant directly on albumID - i.e. who this album has been shared
// with, not including the viewer's own (usually admin-granted) access to
// it. Callers must already have verified the viewer is allowed to see this
// (the album's owner, or an admin) - this function itself does no
// authorization check.
func AlbumPermissions(db *gorm.DB, albumID int, viewerID int) ([]*models.AlbumPermission, error) {
	var rows []models.UserAlbums
	if err := db.Where("album_id = ? AND user_id != ?", albumID, viewerID).Find(&rows).Error; err != nil {
		return nil, err
	}

	permissions := make([]*models.AlbumPermission, 0, len(rows))
	if len(rows) == 0 {
		return permissions, nil
	}

	// One query for every grantee rather than one per grant: the settings
	// page resolves this field for every root album of every user, so a
	// per-row lookup multiplies out across the whole table.
	userIDs := make([]int, 0, len(rows))
	for _, row := range rows {
		userIDs = append(userIDs, row.UserID)
	}

	var users []*models.User
	if err := db.Where("id IN ?", userIDs).Find(&users).Error; err != nil {
		return nil, err
	}

	usersByID := make(map[int]*models.User, len(users))
	for _, user := range users {
		usersByID[user.ID] = user
	}

	for _, row := range rows {
		user, ok := usersByID[row.UserID]
		if !ok {
			continue
		}
		permissions = append(permissions, &models.AlbumPermission{
			User:  user,
			Level: row.Level,
		})
	}

	return permissions, nil
}

// ShareAlbum grants targetUserID access to albumID at level, on behalf of
// actor. Only albumID's owner (an explicit grant with no GrantedByUserID,
// i.e. it traces to an admin grant rather than another user's share) may
// do this, and only up to their own level - admins bypass both checks.
// Re-sharing an already-shared album updates the level and re-propagates it
// across the album's current subtree, so already-scanned descendants never
// keep a stale level.
func GrantAlbumAccess(db *gorm.DB, actor *models.User, albumID int, targetUserID int, level models.AlbumPermissionLevel) (*models.AlbumPermission, error) {
	if !actor.Admin && !actor.CanShare {
		return nil, errors.New("not allowed to share albums")
	}

	if actor.ID == targetUserID {
		return nil, errors.New("cannot share an album with yourself")
	}

	var album models.Album
	if err := db.First(&album, albumID).Error; err != nil {
		return nil, err
	}

	if !actor.Admin {
		grant, err := actor.EffectiveGrant(db, &album)
		if err != nil {
			return nil, err
		}
		if grant == nil || grant.GrantedByUserID != nil {
			return nil, errors.New("only the owner of a folder may share it")
		}
		if level.HigherThan(grant.Level) {
			return nil, errors.New("cannot grant a higher permission level than your own")
		}
	}

	if !actor.Admin {
		foreign, err := targetGrantIsForeign(db, actor.ID, albumID, targetUserID)
		if err != nil {
			return nil, err
		}
		if foreign {
			return nil, errors.New("that user's access to this folder was granted by someone else")
		}
	}

	var targetUser models.User
	if err := db.First(&targetUser, targetUserID).Error; err != nil {
		return nil, errors.Wrap(err, "find target user")
	}

	if err := models.PropagateAlbumLevel(db, albumID, targetUserID, level, &actor.ID); err != nil {
		return nil, err
	}

	return &models.AlbumPermission{User: &targetUser, Level: level}, nil
}

// RevokeAlbumAccess removes targetUserID's access to albumID (and every
// current descendant), on behalf of actor. Only the album's owner or an
// admin may do this.
func RevokeAlbumAccess(db *gorm.DB, actor *models.User, albumID int, targetUserID int) error {
	if actor.ID == targetUserID {
		return errors.New("cannot revoke your own access")
	}

	var album models.Album
	if err := db.First(&album, albumID).Error; err != nil {
		return err
	}

	if !actor.Admin {
		grant, err := actor.EffectiveGrant(db, &album)
		if err != nil {
			return err
		}
		if grant == nil || grant.GrantedByUserID != nil {
			return errors.New("only the owner of a folder may revoke access to it")
		}

		foreign, err := targetGrantIsForeign(db, actor.ID, albumID, targetUserID)
		if err != nil {
			return err
		}
		if foreign {
			return errors.New("that user's access to this folder was granted by someone else")
		}
	}

	return models.RevokeAlbumLevel(db, albumID, targetUserID)
}

// targetGrantIsForeign reports whether targetUserID already holds a grant on
// albumID, sourced at albumID itself, that actorID has no business touching:
// one an admin configured (no grantor at all) or one another user shared.
// Grant and revoke both write exactly that row - the upsert key is
// (user, album, source_album) - so without this check one owner could
// overwrite a co-owner's admin-configured grant, or revoke access they
// never gave. Re-sharing or withdrawing the actor's own earlier share still
// passes, since that row names them as the grantor.
func targetGrantIsForeign(db *gorm.DB, actorID int, albumID int, targetUserID int) (bool, error) {
	var grant models.UserAlbumGrant
	err := db.Where("user_id = ? AND album_id = ? AND source_album_id = ?", targetUserID, albumID, albumID).First(&grant).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if err != nil {
		return false, errors.Wrap(err, "get target user's existing grant on album")
	}

	return grant.GrantedByUserID == nil || *grant.GrantedByUserID != actorID, nil
}
