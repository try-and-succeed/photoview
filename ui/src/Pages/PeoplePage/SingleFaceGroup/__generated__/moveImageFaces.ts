/* tslint:disable */
/* eslint-disable */
// @generated
// This file was automatically generated and should not be edited.

// ====================================================
// GraphQL mutation operation: moveImageFaces
// ====================================================

export interface moveImageFaces_moveImageFaces_imageFaces {
  __typename: "ImageFace";
  id: string;
}

export interface moveImageFaces_moveImageFaces {
  __typename: "FaceGroup";
  id: string;
  imageFaces: moveImageFaces_moveImageFaces_imageFaces[];
}

export interface moveImageFaces {
  /**
   * Move a list of ImageFaces to another face group. It fails when the group
   * would then hold two faces on one of the moved faces' photos, unless
   * allowDuplicates is set: a person can appear twice on a photo, for example on
   * a photographed album page or a sheet of passport pictures.
   */
  moveImageFaces: moveImageFaces_moveImageFaces;
}

export interface moveImageFacesVariables {
  faceIDs: string[];
  destFaceGroupID: string;
  allowDuplicates?: boolean | null;
}
