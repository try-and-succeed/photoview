import { vi } from 'vitest'
import { render, fireEvent, waitFor, screen } from '@testing-library/react'
import { MockedProvider } from '@apollo/client/testing'
import { GraphQLError } from 'graphql'
import MoveImageFacesModal, {
  MOVE_IMAGE_FACES_MUTATION,
} from './MoveImageFacesModal'
import { MY_FACES_QUERY } from '../PeoplePage'

const navigate = vi.fn()
vi.mock('react-router-dom', async () => {
  const actual: object = await vi.importActual('react-router-dom')
  return { ...actual, useNavigate: () => navigate }
})

beforeAll(() => {
  global.IntersectionObserver = class {
    observe() {}
    unobserve() {}
    disconnect() {}
  } as any
})

vi.mock('./SelectFaceGroupTable', () => ({
  __esModule: true,
  default: ({ faceGroups, setSelectedFaceGroup }: any) => (
    <div>
      {faceGroups.map((fg: any) => (
        <button
          key={fg.id}
          data-testid={`facegroup-${fg.id}`}
          onClick={() => setSelectedFaceGroup(fg)}
        >
          {fg.label}
        </button>
      ))}
    </div>
  ),
}))

const myFacesMock = {
  request: { query: MY_FACES_QUERY },
  result: {
    data: {
      myFaceGroups: [
        {
          __typename: 'FaceGroup',
          id: '2',
          label: 'Fabian',
          imageFaceCount: 1,
          imageFaces: [],
        },
      ],
    },
  },
}

function moveMock(allowDuplicates: boolean, fails: boolean) {
  return {
    request: {
      query: MOVE_IMAGE_FACES_MUTATION,
      variables: { faceIDs: ['7'], destFaceGroupID: '2', allowDuplicates },
    },
    result: fails
      ? {
          errors: [
            new GraphQLError(
              'cannot move faces because the destination would contain duplicate images'
            ),
          ],
        }
      : {
          data: {
            moveImageFaces: {
              __typename: 'FaceGroup',
              id: '2',
              imageFaces: [{ __typename: 'ImageFace', id: '7' }],
            },
          },
        },
  }
}

function renderModal(mocks: any[]) {
  const face = { __typename: 'ImageFace', id: '7' } as any
  render(
    <MockedProvider mocks={mocks} addTypename={false}>
      <MoveImageFacesModal
        open={true}
        setOpen={vi.fn()}
        faceGroup={{ __typename: 'FaceGroup', id: '1', imageFaces: [] } as any}
        preselectedImageFaces={[face]}
      />
    </MockedProvider>
  )
}

async function moveToFabian() {
  fireEvent.click(await screen.findByTestId('facegroup-2'))
  fireEvent.click(screen.getByText('Move image faces'))
}

describe('MoveImageFacesModal', () => {
  beforeEach(() => {
    navigate.mockReset()
  })

  test('asks before moving a face onto a photo the person is already on', async () => {
    const confirm = vi.spyOn(window, 'confirm').mockReturnValue(true)
    renderModal([myFacesMock, moveMock(false, true), moveMock(true, false)])

    await moveToFabian()

    await waitFor(() => expect(navigate).toHaveBeenCalledWith('/people/2'))
    expect(confirm).toHaveBeenCalledTimes(1)
    confirm.mockRestore()
  })

  test('keeps the faces where they are when the user declines', async () => {
    const confirm = vi.spyOn(window, 'confirm').mockReturnValue(false)
    renderModal([myFacesMock, moveMock(false, true), moveMock(true, false)])

    await moveToFabian()

    await waitFor(() => expect(confirm).toHaveBeenCalledTimes(1))
    expect(navigate).not.toHaveBeenCalled()
    confirm.mockRestore()
  })
})
