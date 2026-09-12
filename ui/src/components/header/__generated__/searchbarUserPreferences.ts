/* tslint:disable */
/* eslint-disable */
// @generated
// This file was automatically generated and should not be edited.

// ====================================================
// GraphQL query operation: searchbarUserPreferences
// ====================================================

export interface searchbarUserPreferences_myUserPreferences {
  __typename: "UserPreferences";
  id: string;
  /**
   * How many results a search returns per category. 0 means no limit. Unset falls back to the server default.
   */
  searchResultLimit: number | null;
}

export interface searchbarUserPreferences {
  /**
   * User preferences for the logged in user
   */
  myUserPreferences: searchbarUserPreferences_myUserPreferences;
}
