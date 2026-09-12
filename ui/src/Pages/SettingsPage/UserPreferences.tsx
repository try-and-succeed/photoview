import { useMutation, useQuery } from '@apollo/client'
import gql from 'graphql-tag'
import React, { useEffect, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import styled from 'styled-components'
import { LanguageTranslation } from '../../__generated__/globalTypes'
import Dropdown from '../../primitives/form/Dropdown'
import { Button, TextField } from '../../primitives/form/Input'
import {
  InputLabelDescription,
  InputLabelTitle,
  SectionTitle,
} from './SettingsPage'
import {
  changeUserPreferences,
  changeUserPreferencesVariables,
} from './__generated__/changeUserPreferences'
import { myUserPreferences } from './__generated__/myUserPreferences'
import { myUsername } from './__generated__/myUsername'
import { TranslationFn } from '../../localization'
import { changeTheme, getTheme } from '../../theme'

const languagePreferences = [
  { key: 1, label: 'English', value: LanguageTranslation.English },
  { key: 2, label: 'Français', value: LanguageTranslation.French },
  { key: 3, label: 'Svenska', value: LanguageTranslation.Swedish },
  { key: 4, label: 'Dansk', value: LanguageTranslation.Danish },
  { key: 5, label: 'Español', value: LanguageTranslation.Spanish },
  { key: 6, label: 'Polski', value: LanguageTranslation.Polish },
  { key: 7, label: 'Italiano', value: LanguageTranslation.Italian },
  { key: 8, label: 'Deutsch', value: LanguageTranslation.German },
  { key: 9, label: 'Русский', value: LanguageTranslation.Russian },
  { key: 10, label: '繁體中文 (香港)', value: LanguageTranslation.TraditionalChineseHK },
  { key: 16, label: '繁體中文 (台灣)', value: LanguageTranslation.TraditionalChineseTW },
  { key: 11, label: '简体中文', value: LanguageTranslation.SimplifiedChinese },
  { key: 12, label: 'Português', value: LanguageTranslation.Portuguese },
  { key: 13, label: 'Euskara', value: LanguageTranslation.Basque },
  { key: 14, label: 'Türkçe', value: LanguageTranslation.Turkish },
  { key: 15, label: 'Українська', value: LanguageTranslation.Ukrainian },
  { key: 17, label: '日本語', value: LanguageTranslation.Japanese },
  { key: 18, label: 'Nederlands', value: LanguageTranslation.Dutch },
]

const themePreferences = (t: TranslationFn) => [
  {
    key: 1,
    label: t('settings.user_preferences.theme.auto.label', 'Same as system'),
    value: 'auto',
  },
  {
    key: 2,
    label: t('settings.user_preferences.theme.light.label', 'Light'),
    value: 'light',
  },
  {
    key: 2,
    label: t('settings.user_preferences.theme.dark.label', 'Dark'),
    value: 'dark',
  },
]

const CHANGE_USER_PREFERENCES = gql`
  mutation changeUserPreferences($language: String, $searchResultLimit: Int) {
    changeUserPreferences(
      language: $language
      searchResultLimit: $searchResultLimit
    ) {
      id
      language
      searchResultLimit
    }
  }
`

const MY_USER_PREFERENCES = gql`
  query myUserPreferences {
    myUserPreferences {
      id
      language
      searchResultLimit
    }
  }
`

const MY_USERNAME_QUERY = gql`
  query myUsername {
    myUser {
      id
      username
    }
  }
`

const LogoutButton = () => {
  const { t } = useTranslation()

  return (
    <Button
      className="mb-4"
      onClick={() => {
        location.href = '/logout'
      }}
    >
      {t('settings.logout', 'Log out')}
    </Button>
  )
}

const UserPreferencesWrapper = styled.div`
  margin-bottom: 24px;
`

const UserPreferences = () => {
  const { t } = useTranslation()
  const [theme, setTheme] = useState(getTheme())

  const changeStateTheme = (value: string) => {
    changeTheme(value)
    setTheme(value)
  }

  const { data } = useQuery<myUserPreferences>(MY_USER_PREFERENCES)
  const { data: usernameData } = useQuery<myUsername>(MY_USERNAME_QUERY)

  const [changePrefs, { loading: loadingPrefs, error }] = useMutation<
    changeUserPreferences,
    changeUserPreferencesVariables
  >(CHANGE_USER_PREFERENCES)

  const sortedLanguagePrefs = useMemo(
    () => [...languagePreferences].sort((a, b) => a.label.localeCompare(b.label)),
    []
  )

  const savedSearchLimit = data?.myUserPreferences.searchResultLimit
  const [searchLimitInput, setSearchLimitInput] = useState('')

  useEffect(() => {
    setSearchLimitInput(
      savedSearchLimit == null ? '' : String(savedSearchLimit)
    )
  }, [savedSearchLimit])

  const commitSearchLimit = () => {
    const trimmed = searchLimitInput.trim()
    // An empty field means "whatever the server does by default", which is
    // also the state of a user who never touched this - so there is nothing
    // to save.
    if (trimmed === '') return

    const parsed = Number(trimmed)
    if (!Number.isInteger(parsed) || parsed < 0) {
      setSearchLimitInput(
        savedSearchLimit == null ? '' : String(savedSearchLimit)
      )
      return
    }

    if (parsed === savedSearchLimit) return

    changePrefs({ variables: { searchResultLimit: parsed } })
  }

  if (error) {
    return <div>{error.message}</div>
  }

  return (
    <UserPreferencesWrapper>
      <SectionTitle nospace>
        {usernameData?.myUser
          ? t(
              'settings.user_preferences.title_with_username',
              'User preferences ({{username}})',
              { username: usernameData.myUser.username }
            )
          : t('settings.user_preferences.title', 'User preferences')}
      </SectionTitle>
      <LogoutButton />
      <label htmlFor="user_pref_change_language_field">
        <InputLabelTitle>
          {t(
            'settings.user_preferences.change_language.label',
            'Website language'
          )}
        </InputLabelTitle>
        <InputLabelDescription>
          {t(
            'settings.user_preferences.change_language.description',
            'Change website language specific for this user'
          )}
        </InputLabelDescription>
      </label>
      <Dropdown
        id="user_pref_change_language_field"
        placeholder={t(
          'settings.user_preferences.language_selector.placeholder',
          'Select language'
        )}
        items={sortedLanguagePrefs}
        setSelected={language => {
          changePrefs({
            variables: {
              language: language as LanguageTranslation,
            },
          })
        }}
        selected={data?.myUserPreferences.language || undefined}
        disabled={loadingPrefs}
      />
      <label htmlFor="user_pref_change_theme_field">
        <InputLabelTitle>
          {t('settings.user_preferences.theme.title', 'Theme preferences')}
        </InputLabelTitle>
        <InputLabelDescription>
          {t(
            'settings.user_preferences.theme.description',
            'Change the appearance of the website'
          )}
        </InputLabelDescription>
      </label>
      <Dropdown
        id="user_pref_change_theme_field"
        items={themePreferences(t)}
        setSelected={changeStateTheme}
        selected={theme}
      />
      <label htmlFor="user_pref_search_result_limit_field">
        <InputLabelTitle>
          {t(
            'settings.user_preferences.search_result_limit.label',
            'Search results'
          )}
        </InputLabelTitle>
        <InputLabelDescription>
          {t(
            'settings.user_preferences.search_result_limit.description',
            'How many albums and how many media files a search returns. Enter 0 to show every match.'
          )}
        </InputLabelDescription>
      </label>
      <TextField
        id="user_pref_search_result_limit_field"
        type="number"
        min={0}
        step={1}
        placeholder="10"
        value={searchLimitInput}
        disabled={loadingPrefs}
        onChange={e => setSearchLimitInput(e.target.value)}
        onBlur={commitSearchLimit}
        action={commitSearchLimit}
      />
    </UserPreferencesWrapper>
  )
}

export default UserPreferences
