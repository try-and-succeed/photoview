/* tslint:disable */
/* eslint-disable */
// @generated
// This file was automatically generated and should not be edited.

// ====================================================
// GraphQL mutation operation: scanAlbum
// ====================================================

export interface scanAlbum_scanAlbum {
  __typename: "ScannerResult";
  success: boolean;
}

export interface scanAlbum {
  /**
   * Recursively scan a single album and its sub-albums for new media, without
   * scanning the rest of the library. Sub-albums whose directories no longer
   * exist are removed. Caller must be an admin or own the album.
   * 
   * With `onlyNewSubAlbums`, the album's own media are scanned, and of its
   * sub-directories only the direct ones that are not albums yet are taken in,
   * each with everything below it. Existing sub-albums are neither scanned nor
   * removed.
   */
  scanAlbum: scanAlbum_scanAlbum;
}

export interface scanAlbumVariables {
  albumId: string;
  onlyNewSubAlbums?: boolean | null;
}
