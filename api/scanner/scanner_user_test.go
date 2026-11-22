package scanner_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/photoview/photoview/api/graphql/models"
	"github.com/photoview/photoview/api/scanner"
	"github.com/photoview/photoview/api/scanner/scanner_cache"
	"github.com/photoview/photoview/api/test_utils"
	"github.com/stretchr/testify/assert"
)

// writeDummyPhoto copies a real (tiny) fixture image into dir/name - the
// scanner's media-type detection sniffs file content, not just the extension,
// so an arbitrary non-empty file isn't recognized as a photo and its directory
// never becomes an album.
func writeDummyPhoto(t *testing.T, dir, name string) {
	t.Helper()
	content, err := os.ReadFile("./test_media/orient/up_arrow_90cw_web.jpg")
	assert.NoError(t, err)
	assert.NoError(t, os.WriteFile(filepath.Join(dir, name), content, 0o644))
}

// TestFindAlbumsForAlbumRemovesVanishedSubAlbums covers moving a directory out
// of an album on disk: a rescan of that one album has to drop the album row the
// directory left behind, while leaving everything outside the scanned subtree
// alone.
func TestFindAlbumsForAlbumRemovesVanishedSubAlbums(t *testing.T) {
	db := test_utils.DatabaseTest(t)
	test_utils.FilesystemTest(t)

	rootPath := t.TempDir()
	root := models.Album{Title: "root", Path: rootPath}
	assert.NoError(t, db.Save(&root).Error)

	movedPath := filepath.Join(rootPath, "moved")
	assert.NoError(t, os.Mkdir(movedPath, 0o755))
	writeDummyPhoto(t, movedPath, "photo.jpg")

	stayingPath := filepath.Join(rootPath, "staying")
	assert.NoError(t, os.Mkdir(stayingPath, 0o755))
	writeDummyPhoto(t, stayingPath, "photo.jpg")

	// Never part of the scanned subtree, so the rescan never looks at its
	// directory and must not draw any conclusion about it.
	outsider := models.Album{Title: "outsider", Path: t.TempDir()}
	assert.NoError(t, db.Save(&outsider).Error)

	_, scanErrors := scanner.FindAlbumsForAlbum(db, &root, scanner_cache.MakeAlbumCache())
	assert.Empty(t, scanErrors)

	var moved models.Album
	assert.NoError(t, db.Where("path = ?", movedPath).First(&moved).Error)

	// The move itself - all this album's directory knows is that it's gone.
	assert.NoError(t, os.RemoveAll(movedPath))

	_, scanErrors = scanner.FindAlbumsForAlbum(db, &root, scanner_cache.MakeAlbumCache())
	assert.Empty(t, scanErrors)

	var count int64
	assert.NoError(t, db.Model(&models.Album{}).Where("id = ?", moved.ID).Count(&count).Error)
	assert.Zero(t, count, "a sub-album whose directory is gone must not survive a rescan of its parent")

	assert.NoError(t, db.Model(&models.Album{}).Where("path = ?", stayingPath).Count(&count).Error)
	assert.Equal(t, int64(1), count, "the sub-album still on disk must be kept")

	assert.NoError(t, db.Model(&models.Album{}).Where("id = ?", root.ID).Count(&count).Error)
	assert.Equal(t, int64(1), count, "the rescanned album itself must never be deleted")

	assert.NoError(t, db.Model(&models.Album{}).Where("id = ?", outsider.ID).Count(&count).Error)
	assert.Equal(t, int64(1), count, "an album outside the scanned subtree must be left alone")
}

// TestFindAlbumsForUserKeepsAlbumsWhenDiscoveryFails covers a root directory
// that can't be read - an unmounted share, say. Everything below it is missing
// from the walk's result, which the cleanup would otherwise read as "deleted".
func TestFindAlbumsForUserKeepsAlbumsWhenDiscoveryFails(t *testing.T) {
	db := test_utils.DatabaseTest(t)
	test_utils.FilesystemTest(t)

	user, err := models.RegisterUser(db, "incomplete_scan_user", nil, false)
	assert.NoError(t, err)

	readablePath := t.TempDir()
	readable := models.Album{Title: "readable", Path: readablePath}
	assert.NoError(t, db.Save(&readable).Error)
	writeDummyPhoto(t, readablePath, "photo.jpg")

	unreadablePath := t.TempDir()
	unreadable := models.Album{Title: "unreadable", Path: unreadablePath}
	assert.NoError(t, db.Save(&unreadable).Error)

	assert.NoError(t, db.Model(&user).Association("Albums").Append(&readable, &unreadable))

	// Not IsNotExist: the directory is there, we just can't look inside it.
	assert.NoError(t, os.Chmod(unreadablePath, 0o000))
	t.Cleanup(func() { os.Chmod(unreadablePath, 0o755) })

	// Permissions don't apply to a root-capable process, which is how tests
	// often run in a container - there the scan would succeed and there would
	// be no incomplete discovery to assert about.
	if _, err := os.ReadDir(unreadablePath); err == nil {
		t.Skip("this process can read a 0o000 directory, so discovery cannot fail here")
	}

	_, scanErrors := scanner.FindAlbumsForUser(db, user, scanner_cache.MakeAlbumCache())
	assert.NotEmpty(t, scanErrors, "the unreadable directory should be reported")

	var count int64
	assert.NoError(t, db.Model(&models.Album{}).Where("id = ?", unreadable.ID).Count(&count).Error)
	assert.Equal(t, int64(1), count, "an album must not be deleted just because the scan couldn't read it")
}

func TestFindAlbumsForAlbumKeepsSubAlbumsWhenOneCannotBeRead(t *testing.T) {
	db := test_utils.DatabaseTest(t)
	test_utils.FilesystemTest(t)

	rootPath := t.TempDir()
	writeDummyPhoto(t, rootPath, "photo.jpg")
	root := models.Album{Title: "root", Path: rootPath}
	assert.NoError(t, db.Save(&root).Error)

	unreadablePath := filepath.Join(rootPath, "unreadable")
	assert.NoError(t, os.Mkdir(unreadablePath, 0o755))
	writeDummyPhoto(t, unreadablePath, "photo.jpg")
	unreadable := models.Album{Title: "unreadable", Path: unreadablePath, ParentAlbumID: &root.ID}
	assert.NoError(t, db.Save(&unreadable).Error)

	// The directory is there with media in it; the scan just cannot look
	// inside. Reporting that as "no photos here" would leave the walk looking
	// complete, and the cleanup would then delete an album that is still on
	// disk.
	assert.NoError(t, os.Chmod(unreadablePath, 0o000))
	t.Cleanup(func() { os.Chmod(unreadablePath, 0o755) })

	if _, err := os.ReadDir(unreadablePath); err == nil {
		t.Skip("this process can read a 0o000 directory, so discovery cannot fail here")
	}

	_, scanErrors := scanner.FindAlbumsForAlbum(db, &root, scanner_cache.MakeAlbumCache())
	assert.NotEmpty(t, scanErrors, "the unreadable sub-directory should be reported")

	var count int64
	assert.NoError(t, db.Model(&models.Album{}).Where("id = ?", unreadable.ID).Count(&count).Error)
	assert.Equal(t, int64(1), count,
		"a sub-album must not be deleted just because the scan couldn't read its directory")
}

// TestFindNewSubAlbumsForAlbum adds a folder next to an existing one: the album
// itself comes back for its own photos, and of the sub-directories only the new
// one and what is below it become albums. The existing sub-album is not walked,
// so a folder added inside it stays unseen, and a sub-album whose directory is
// gone is not removed.
func TestFindNewSubAlbumsForAlbum(t *testing.T) {
	db := test_utils.DatabaseTest(t)
	test_utils.FilesystemTest(t)

	rootPath := t.TempDir()
	writeDummyPhoto(t, rootPath, "photo.jpg")
	root := models.Album{Title: "root", Path: rootPath}
	assert.NoError(t, db.Save(&root).Error)

	oldPath := filepath.Join(rootPath, "old")
	assert.NoError(t, os.Mkdir(oldPath, 0o755))
	writeDummyPhoto(t, oldPath, "photo.jpg")
	old := models.Album{Title: "old", Path: oldPath, ParentAlbumID: &root.ID}
	assert.NoError(t, db.Save(&old).Error)

	deeperPath := filepath.Join(oldPath, "deeper")
	assert.NoError(t, os.Mkdir(deeperPath, 0o755))
	writeDummyPhoto(t, deeperPath, "photo.jpg")

	vanished := models.Album{Title: "vanished", Path: filepath.Join(rootPath, "vanished"), ParentAlbumID: &root.ID}
	assert.NoError(t, db.Save(&vanished).Error)

	newPath := filepath.Join(rootPath, "new")
	innerPath := filepath.Join(newPath, "inner")
	assert.NoError(t, os.MkdirAll(innerPath, 0o755))
	writeDummyPhoto(t, innerPath, "photo.jpg")

	emptyPath := filepath.Join(rootPath, "empty")
	assert.NoError(t, os.Mkdir(emptyPath, 0o755))

	albums, scanErrors := scanner.FindNewSubAlbumsForAlbum(db, &root, scanner_cache.MakeAlbumCache())
	assert.Empty(t, scanErrors)

	paths := make([]string, len(albums))
	for i, album := range albums {
		paths[i] = album.Path
	}
	if assert.NotEmpty(t, paths) {
		assert.Equal(t, rootPath, paths[0], "the album itself comes first, for its own photos")
	}
	assert.ElementsMatch(t, []string{rootPath, newPath, innerPath}, paths,
		"besides the album, only the new folder and what is below it are taken in")

	var created models.Album
	if assert.NoError(t, db.Where("path = ?", newPath).First(&created).Error) && assert.NotNil(t, created.ParentAlbumID) {
		assert.Equal(t, root.ID, *created.ParentAlbumID)
	}

	var count int64
	assert.NoError(t, db.Model(&models.Album{}).Where("path = ?", deeperPath).Count(&count).Error)
	assert.Zero(t, count, "an existing sub-album is not walked")

	assert.NoError(t, db.Model(&models.Album{}).Where("id = ?", vanished.ID).Count(&count).Error)
	assert.Equal(t, int64(1), count, "looking for new folders removes nothing")

	albums, scanErrors = scanner.FindNewSubAlbumsForAlbum(db, &root, scanner_cache.MakeAlbumCache())
	assert.Empty(t, scanErrors)
	if assert.Len(t, albums, 1, "a second run finds nothing new") {
		assert.Equal(t, root.ID, albums[0].ID)
	}
}

func TestFindNewSubAlbumsForAlbumReportsAMissingDirectory(t *testing.T) {
	db := test_utils.DatabaseTest(t)
	test_utils.FilesystemTest(t)

	gone := models.Album{Title: "gone", Path: filepath.Join(t.TempDir(), "gone")}
	assert.NoError(t, db.Save(&gone).Error)

	albums, scanErrors := scanner.FindNewSubAlbumsForAlbum(db, &gone, scanner_cache.MakeAlbumCache())
	assert.NotEmpty(t, scanErrors)
	assert.Empty(t, albums)
}
