package resolvers

import (
	"context"
	"testing"

	"github.com/photoview/photoview/api/graphql/auth"
	"github.com/photoview/photoview/api/graphql/models"
	"github.com/photoview/photoview/api/test_utils"
	"github.com/stretchr/testify/assert"
)

func TestScanAlbumAuthorization(t *testing.T) {
	db := test_utils.DatabaseTest(t)

	// The real queue starts a background worker on first use and walks the
	// filesystem; this test only cares who gets past the permission check.
	queued := make([]int, 0)
	origAddAlbumToQueue := addAlbumToQueue
	addAlbumToQueue = func(album *models.Album) error {
		queued = append(queued, album.ID)
		return nil
	}
	t.Cleanup(func() { addAlbumToQueue = origAddAlbumToQueue })

	album := models.Album{Title: "scan_album", Path: "/photos/scan_album"}
	assert.NoError(t, db.Save(&album).Error)

	owner, err := models.RegisterUser(db, "scan_owner", nil, false)
	assert.NoError(t, err)
	assert.NoError(t, db.Model(&owner).Association("Albums").Append(&album))

	stranger, err := models.RegisterUser(db, "scan_stranger", nil, false)
	assert.NoError(t, err)

	admin, err := models.RegisterUser(db, "scan_admin", nil, true)
	assert.NoError(t, err)

	r := &mutationResolver{Resolver: &Resolver{database: db}}

	t.Run("the album's owner may rescan it", func(t *testing.T) {
		queued = queued[:0]

		result, err := r.ScanAlbum(auth.AddUserToContext(context.Background(), owner), album.ID)
		assert.NoError(t, err)
		if assert.NotNil(t, result) {
			assert.True(t, result.Success)
		}
		assert.Equal(t, []int{album.ID}, queued)
	})

	t.Run("a user without access may not", func(t *testing.T) {
		queued = queued[:0]

		_, err := r.ScanAlbum(auth.AddUserToContext(context.Background(), stranger), album.ID)
		assert.Error(t, err)
		assert.Empty(t, queued, "a denied request must not reach the queue")
	})

	t.Run("an admin may rescan any album", func(t *testing.T) {
		queued = queued[:0]

		_, err := r.ScanAlbum(auth.AddUserToContext(context.Background(), admin), album.ID)
		assert.NoError(t, err)
		assert.Equal(t, []int{album.ID}, queued)
	})

	t.Run("an unauthenticated request is refused", func(t *testing.T) {
		_, err := r.ScanAlbum(context.Background(), album.ID)
		assert.Error(t, err)
	})
}

func TestScannerQueueVisibilityAndCancel(t *testing.T) {
	db := test_utils.DatabaseTest(t)

	ownedAlbum := models.Album{Title: "owned", Path: "/photos/owned"}
	assert.NoError(t, db.Save(&ownedAlbum).Error)
	foreignAlbum := models.Album{Title: "foreign", Path: "/photos/foreign"}
	assert.NoError(t, db.Save(&foreignAlbum).Error)

	owner, err := models.RegisterUser(db, "queue_owner", nil, false)
	assert.NoError(t, err)
	assert.NoError(t, db.Model(&owner).Association("Albums").Append(&ownedAlbum))

	admin, err := models.RegisterUser(db, "queue_admin", nil, true)
	assert.NoError(t, err)

	origStatus := getScannerQueueStatus
	getScannerQueueStatus = func() []models.ScannerQueueItem {
		return []models.ScannerQueueItem{
			{Album: &ownedAlbum, Status: models.ScannerJobStatusRunning},
			{Album: &foreignAlbum, Status: models.ScannerJobStatusQueued},
		}
	}
	t.Cleanup(func() { getScannerQueueStatus = origStatus })

	cancelled := make([]int, 0)
	origCancel := cancelScanJob
	cancelScanJob = func(albumID int) bool {
		cancelled = append(cancelled, albumID)
		return true
	}
	t.Cleanup(func() { cancelScanJob = origCancel })

	allCancelled := 0
	origCancelAll := cancelAllScanJobs
	cancelAllScanJobs = func() int {
		allCancelled++
		return 7
	}
	t.Cleanup(func() { cancelAllScanJobs = origCancelAll })

	q := &queryResolver{Resolver: &Resolver{database: db}}
	m := &mutationResolver{Resolver: &Resolver{database: db}}

	t.Run("a user only sees jobs for albums they own", func(t *testing.T) {
		items, err := q.ScannerQueueStatus(auth.AddUserToContext(context.Background(), owner))
		assert.NoError(t, err)
		if assert.Len(t, items, 1) {
			assert.Equal(t, ownedAlbum.ID, items[0].Album.ID)
			assert.Equal(t, models.ScannerJobStatusRunning, items[0].Status)
		}
	})

	t.Run("an admin sees every job", func(t *testing.T) {
		items, err := q.ScannerQueueStatus(auth.AddUserToContext(context.Background(), admin))
		assert.NoError(t, err)
		assert.Len(t, items, 2)
	})

	t.Run("cancelling someone else's job is refused", func(t *testing.T) {
		cancelled = cancelled[:0]

		_, err := m.CancelScanJob(auth.AddUserToContext(context.Background(), owner), foreignAlbum.ID)
		assert.Error(t, err)
		assert.Empty(t, cancelled, "a denied request must not reach the queue")
	})

	t.Run("cancel all only reaches a user's own jobs", func(t *testing.T) {
		cancelled = cancelled[:0]

		count, err := m.CancelAllScanJobs(auth.AddUserToContext(context.Background(), owner))
		assert.NoError(t, err)
		assert.Equal(t, 1, count)
		assert.Equal(t, []int{ownedAlbum.ID}, cancelled)
		assert.Zero(t, allCancelled, "a non-admin must not clear the whole queue")
	})

	t.Run("an admin clears the whole queue in one go", func(t *testing.T) {
		count, err := m.CancelAllScanJobs(auth.AddUserToContext(context.Background(), admin))
		assert.NoError(t, err)
		assert.Equal(t, 7, count)
		assert.Equal(t, 1, allCancelled)
	})
}
