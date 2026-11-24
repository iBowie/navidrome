import React, { useEffect, useState } from 'react'
import { useDispatch, useSelector } from 'react-redux'
import {
  Button,
  Checkbox,
  Dialog,
  DialogActions,
  DialogContent,
  DialogTitle,
  FormControlLabel,
  MenuItem,
  TextField,
} from '@material-ui/core'
import httpClient from '../dataProvider/httpClient'
import { closeLibraryDataDialog } from '../actions'
import { EntityPicker } from '../librarydata/EntityPicker'

const LINK_KINDS = ['version', 'alternate', 'counterpart']
const TYPE_SUGGESTIONS = [
  'compilation',
  'soundtrack',
  'single',
  'ep',
  'live',
  'remix',
  'bootleg',
]

const KIND_FIELDS = {
  songs: ['title', 'artist', 'lyrics'],
  albums: ['name', 'artist'],
  artists: ['name'],
}

const useJson = () => {
  const get = async (url) => (await httpClient(url)).json
  const put = async (url, body) => {
    await httpClient(url, { method: 'PUT', body: JSON.stringify(body) })
  }
  return { get, put }
}

/** Link two songs (optionally scoped to an album/playlist). */
const LinkView = ({ payload, onClose }) => {
  const { get, put } = useJson()
  const [source, setSource] = useState(payload.source)
  const [target, setTarget] = useState(null)
  const [kind, setKind] = useState('version')
  const [label, setLabel] = useState('')
  const [merge, setMerge] = useState(false)
  const [error, setError] = useState('')
  const scope = payload.scope // {id, kind: 'album'|'playlist', name} | undefined

  const save = async () => {
    try {
      const r = await get(
        '/api/librarydata/links?ids=' + encodeURIComponent(source.id),
      )
      const links = r.links[source.id] || []
      links.push({
        a: source.id,
        b: target.id,
        kind,
        label: label.trim() || undefined,
        context: scope ? [scope.id] : undefined,
        merge: merge || undefined,
      })
      await put('/api/librarydata/links', { id: source.id, links })
      onClose()
    } catch (e) {
      setError(e.message)
    }
  }

  return (
    <>
      <DialogContent>
        {!payload.source && (
          <div style={{ marginBottom: 12 }}>
            <EntityPicker resource="song" label="Song" value={source} onPick={setSource} />
          </div>
        )}
        <div style={{ display: 'flex', flexWrap: 'wrap', gap: 12 }}>
          <EntityPicker
            resource="song"
            label="Link to song"
            value={target}
            onPick={setTarget}
            excludeId={source?.id}
          />
          <TextField
            select
            label="Kind"
            value={kind}
            onChange={(e) => setKind(e.target.value)}
            style={{ minWidth: 140 }}
          >
            {LINK_KINDS.map((k) => (
              <MenuItem key={k} value={k}>
                {k}
              </MenuItem>
            ))}
          </TextField>
          <TextField
            label="Label (optional)"
            value={label}
            onChange={(e) => setLabel(e.target.value)}
          />
          <FormControlLabel
            control={
              <Checkbox checked={merge} onChange={(e) => setMerge(e.target.checked)} />
            }
            label="Merge into one line"
          />
        </div>
        {scope && (
          <p style={{ marginTop: 12 }}>
            The link shows only on {scope.kind} “{scope.name}”.
          </p>
        )}
        {error && <p style={{ color: 'red' }}>{error}</p>}
      </DialogContent>
      <DialogActions>
        <Button onClick={onClose}>Cancel</Button>
        <Button
          variant="contained"
          color="primary"
          disabled={!source || !target}
          onClick={save}
        >
          Save
        </Button>
      </DialogActions>
    </>
  )
}

/** Edit localized tags for one song/album/artist. */
const LocalizationView = ({ payload, onClose }) => {
  const { get, put } = useJson()
  const [lang, setLang] = useState('')
  const [values, setValues] = useState({})
  const [error, setError] = useState('')
  const fields = KIND_FIELDS[payload.entityKind]

  useEffect(() => {
    setValues({})
  }, [payload])

  const load = async () => {
    try {
      const r = await get(
        `/api/librarydata/localization?lang=${encodeURIComponent(lang.trim())}&kind=${payload.entityKind}&id=${encodeURIComponent(payload.id)}`,
      )
      setValues(r.values || {})
      setError('')
    } catch (e) {
      setError(e.message)
    }
  }
  const save = async () => {
    try {
      await put('/api/librarydata/localization', {
        lang: lang.trim(),
        kind: payload.entityKind,
        id: payload.id,
        values: Object.fromEntries(
          Object.entries(values).filter(([, v]) => v !== ''),
        ),
      })
      onClose()
    } catch (e) {
      setError(e.message)
    }
  }

  return (
    <>
      <DialogContent>
        <p>
          Localized names for “{payload.name}” ({payload.entityKind}).
        </p>
        <div style={{ display: 'flex', flexWrap: 'wrap', gap: 12 }}>
          <TextField
            label="Language (e.g. ja)"
            value={lang}
            onChange={(e) => setLang(e.target.value)}
            style={{ minWidth: 180 }}
          />
          {fields.map((f) => (
            <TextField
              key={f}
              label={f}
              value={values[f] || ''}
              helperText="empty = keep the original"
              onChange={(e) => setValues({ ...values, [f]: e.target.value })}
            />
          ))}
        </div>
        {error && <p style={{ color: 'red' }}>{error}</p>}
      </DialogContent>
      <DialogActions>
        <Button onClick={onClose}>Cancel</Button>
        <Button disabled={!lang.trim()} onClick={load}>
          Load
        </Button>
        <Button
          variant="contained"
          color="primary"
          disabled={!lang.trim()}
          onClick={save}
        >
          Save
        </Button>
      </DialogActions>
    </>
  )
}

/** Set or clear the custom type of an album or playlist. */
const TypeView = ({ payload, onClose }) => {
  const { get, put } = useJson()
  const [type, setType] = useState(payload.type || '')
  const [error, setError] = useState('')

  const save = async () => {
    try {
      const t = await get('/api/librarydata/types')
      const map = t[payload.kind === 'album' ? 'albums' : 'playlists']
      if (type.trim()) map[payload.id] = type.trim()
      else delete map[payload.id]
      await put('/api/librarydata/types', t)
      onClose()
    } catch (e) {
      setError(e.message)
    }
  }

  return (
    <>
      <DialogContent>
        <p>
          Type of “{payload.name}” ({payload.kind}). Empty clears it.
        </p>
        <TextField
          label="Type"
          value={type}
          onChange={(e) => setType(e.target.value)}
          inputProps={{ list: 'ld-type-suggestions' }}
          fullWidth
        />
        <datalist id="ld-type-suggestions">
          {TYPE_SUGGESTIONS.map((t) => (
            <option key={t} value={t} />
          ))}
        </datalist>
        {error && <p style={{ color: 'red' }}>{error}</p>}
      </DialogContent>
      <DialogActions>
        <Button onClick={onClose}>Cancel</Button>
        <Button variant="contained" color="primary" onClick={save}>
          Save
        </Button>
      </DialogActions>
    </>
  )
}

const TITLES = {
  link: 'Link songs',
  localization: 'Localized tags',
  type: 'Custom type',
}

export const LibraryDataDialog = () => {
  const dispatch = useDispatch()
  const state = useSelector((s) => s.libraryDataDialog)
  const onClose = () => dispatch(closeLibraryDataDialog())
  if (!state?.open || !state.view) return null
  return (
    <Dialog open onClose={onClose} fullWidth maxWidth="sm">
      <DialogTitle>{TITLES[state.view]}</DialogTitle>
      {state.view === 'link' && <LinkView payload={state.payload} onClose={onClose} />}
      {state.view === 'localization' && (
        <LocalizationView payload={state.payload} onClose={onClose} />
      )}
      {state.view === 'type' && <TypeView payload={state.payload} onClose={onClose} />}
    </Dialog>
  )
}
