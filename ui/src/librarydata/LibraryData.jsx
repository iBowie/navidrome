import React, { useCallback, useEffect, useMemo, useState } from 'react'
import { Title, useGetList } from 'react-admin'
import {
  Button,
  Card,
  CardContent,
  CardHeader,
  Checkbox,
  Chip,
  Divider,
  FormControlLabel,
  List,
  ListItem,
  ListItemText,
  makeStyles,
  MenuItem,
  Paper,
  TextField,
} from '@material-ui/core'
import httpClient from '../dataProvider/httpClient'
import { EntityPicker } from './EntityPicker'
import { KIND_NAME_FIELD } from './fields'

const useStyles = makeStyles((theme) => ({
  root: { margin: '1em' },
  section: { marginBottom: theme.spacing(2) },
  row: { display: 'flex', flexWrap: 'wrap', gap: theme.spacing(2), marginTop: theme.spacing(1), alignItems: 'flex-end' },
  field: { minWidth: 220, flex: 1 },
  message: { marginTop: theme.spacing(1) },
  chips: { display: 'flex', flexWrap: 'wrap', gap: theme.spacing(1), marginTop: theme.spacing(1) },
}))

const KIND_FIELDS = {
  songs: ['title', 'artist', 'lyrics'],
  albums: ['name', 'artist'],
  artists: ['name'],
}
const KIND_RESOURCE = { songs: 'song', albums: 'album', artists: 'artist' }
const TYPE_SUGGESTIONS = ['compilation', 'soundtrack', 'single', 'ep', 'live', 'remix', 'bootleg']
const LINK_KINDS = ['version', 'alternate', 'counterpart']
const Section = ({ title, children }) => {
  const classes = useStyles()
  return (
    <Card className={classes.section}>
      <CardHeader title={title} />
      <CardContent>{children}</CardContent>
    </Card>
  )
}

const LibraryData = () => {
  const classes = useStyles()
  const [notice, setNotice] = useState(null)
  const notify = (text, isError) => setNotice({ text, isError })

  const get = useCallback(async (url) => (await httpClient(url)).json, [])
  const put = useCallback(async (url, body) => {
    await httpClient(url, { method: 'PUT', body: JSON.stringify(body) })
  }, [])

  // ── Types: pick a playlist or album, give it a type.
  const [typeKind, setTypeKind] = useState('album')
  const [typeEntity, setTypeEntity] = useState(null)
  const [typeValue, setTypeValue] = useState('')
  const saveType = async () => {
    if (!typeEntity) return
    try {
      const t = await get('/api/librarydata/types')
      const map = t[typeKind === 'album' ? 'albums' : 'playlists']
      if (typeValue.trim()) map[typeEntity.id] = typeValue.trim()
      else delete map[typeEntity.id]
      await put('/api/librarydata/types', t)
      notify(`Type ${typeValue.trim() ? 'set' : 'cleared'}`)
    } catch (e) {
      notify('Could not save the type: ' + e.message, true)
    }
  }

  // ── Links: pick the source song, manage its links.
  const [source, setSource] = useState(null)
  const [links, setLinks] = useState(null) // null = not loaded yet
  const [target, setTarget] = useState(null)
  const [linkKind, setLinkKind] = useState('version')
  const [linkLabel, setLinkLabel] = useState('')
  const [linkMerge, setLinkMerge] = useState(false)
  const [context, setContext] = useState([]) // [{id, name, kind: 'playlist'|'album'}]
  useEffect(() => {
    setLinks(null)
    setContext([])
    if (!source) return
    get('/api/librarydata/links?ids=' + encodeURIComponent(source.id))
      .then((r) => setLinks(r.links[source.id] || []))
      .catch((e) => notify('Could not load links: ' + e.message, true))
  }, [source, get])
  const addLink = () => {
    if (!target) return
    setLinks([
      ...(links || []),
      {
        a: source.id,
        b: target.id,
        kind: linkKind,
        label: linkLabel.trim() || undefined,
        context: context.map((c) => c.id),
        merge: linkMerge || undefined,
      },
    ])
    setTarget(null)
    setLinkLabel('')
    setLinkMerge(false)
    setContext([])
  }
  const saveLinks = async () => {
    try {
      await put('/api/librarydata/links', { id: source.id, links: links || [] })
      notify('Links saved')
    } catch (e) {
      notify('Could not save links: ' + e.message, true)
    }
  }

  // ── Localization: pick an entity and language, edit the fields.
  const [locKind, setLocKind] = useState('songs')
  const [locEntity, setLocEntity] = useState(null)
  const [locLang, setLocLang] = useState('')
  const [values, setValues] = useState({})
  const loadLocalization = async () => {
    try {
      const r = await get(
        `/api/librarydata/localization?lang=${encodeURIComponent(locLang.trim())}&kind=${locKind}&id=${encodeURIComponent(locEntity.id)}`
      )
      setValues(r.values || {})
    } catch (e) {
      notify('Could not load: ' + e.message, true)
    }
  }
  const saveLocalization = async () => {
    try {
      await put('/api/librarydata/localization', {
        lang: locLang.trim(),
        kind: locKind,
        id: locEntity.id,
        values: Object.fromEntries(Object.entries(values).filter(([, v]) => v !== '')),
      })
      notify('Localized tags saved')
    } catch (e) {
      notify('Could not save: ' + e.message, true)
    }
  }

  const canLoc = locEntity && locLang.trim() !== ''

  return (
    <div className={classes.root}>
      <Title title="Navidrome - Library data" />
      <Section title="Playlist and album types">
        <div className={classes.row}>
          <TextField select label="What" value={typeKind} onChange={(e) => { setTypeKind(e.target.value); setTypeEntity(null) }} style={{ minWidth: 140 }}>
            <MenuItem value="album">Album</MenuItem>
            <MenuItem value="playlist">Playlist</MenuItem>
          </TextField>
          <EntityPicker resource={typeKind} label={typeKind === 'album' ? 'Album' : 'Playlist'} value={typeEntity} onPick={setTypeEntity} />
          <TextField
            className={classes.field}
            label="Type"
            value={typeValue}
            onChange={(e) => setTypeValue(e.target.value)}
            helperText="empty clears the type"
            inputProps={{ list: 'type-suggestions' }}
          />
          <datalist id="type-suggestions">
            {TYPE_SUGGESTIONS.map((t) => (
              <option key={t} value={t} />
            ))}
          </datalist>
        </div>
        <Button variant="contained" color="primary" disabled={!typeEntity} onClick={saveType} style={{ marginTop: 8 }}>
          Save
        </Button>
      </Section>

      <Section title="Song links">
        <div className={classes.row}>
          <EntityPicker resource="song" label="Song" value={source} onPick={setSource} />
        </div>
        {source && links !== null && (
          <>
            {links.length > 0 && (
              <div className={classes.chips}>
                {links.map((l, i) => (
                  <Chip
                    key={i}
                    label={`→ ${l.b}${l.label ? ' · ' + l.label : ' · ' + l.kind}${l.context?.length ? ' · scoped' : ''}${l.merge ? ' · merged' : ''}`}
                    onDelete={() => setLinks(links.filter((_, n) => n !== i))}
                  />
                ))}
              </div>
            )}
            <Divider style={{ margin: '12px 0' }} />
            <div className={classes.row}>
              <EntityPicker resource="song" label="Link to song" value={target} onPick={setTarget} excludeId={source.id} />
              <TextField select label="Kind" value={linkKind} onChange={(e) => setLinkKind(e.target.value)} style={{ minWidth: 140 }}>
                {LINK_KINDS.map((k) => (
                  <MenuItem key={k} value={k}>
                    {k}
                  </MenuItem>
                ))}
              </TextField>
              <TextField className={classes.field} label="Label (optional)" value={linkLabel} onChange={(e) => setLinkLabel(e.target.value)} />
              <FormControlLabel control={<Checkbox checked={linkMerge} onChange={(e) => setLinkMerge(e.target.checked)} />} label="Merge into one line" />
            </div>
            <div className={classes.row}>
              <EntityPicker
                resource="playlist"
                label="Only on playlist (optional)"
                value={null}
                onPick={(p) => p && !context.some((c) => c.id === p.id) && setContext([...context, { ...p, kind: 'playlist' }])}
              />
              <EntityPicker
                resource="album"
                label="Only on album (optional)"
                value={null}
                onPick={(a) => a && !context.some((c) => c.id === a.id) && setContext([...context, { ...a, kind: 'album' }])}
              />
            </div>
            {context.length > 0 && (
              <div className={classes.chips}>
                {context.map((c) => (
                  <Chip key={c.id} label={`${c.kind}: ${c.name}`} onDelete={() => setContext(context.filter((x) => x.id !== c.id))} />
                ))}
              </div>
            )}
            <Button disabled={!target} onClick={addLink} style={{ marginTop: 8 }}>
              Add link
            </Button>
            <Button variant="contained" color="primary" disabled={links === null} onClick={saveLinks} style={{ marginTop: 8, marginLeft: 8 }}>
              Save
            </Button>
          </>
        )}
      </Section>

      <Section title="Localized tags">
        <div className={classes.row}>
          <TextField
            select
            label="What"
            value={locKind}
            onChange={(e) => {
              setLocKind(e.target.value)
              setLocEntity(null)
              setValues({})
            }}
            style={{ minWidth: 140 }}
          >
            <MenuItem value="songs">Song</MenuItem>
            <MenuItem value="albums">Album</MenuItem>
            <MenuItem value="artists">Artist</MenuItem>
          </TextField>
          <EntityPicker resource={KIND_RESOURCE[locKind]} label="Pick one" value={locEntity} onPick={setLocEntity} />
          <TextField label="Language (e.g. ja)" value={locLang} onChange={(e) => setLocLang(e.target.value)} style={{ minWidth: 180 }} />
        </div>
        <div className={classes.row}>
          {KIND_FIELDS[locKind].map((f) => (
            <TextField
              key={f}
              className={classes.field}
              label={f}
              value={values[f] || ''}
              helperText="empty = keep the original"
              onChange={(e) => setValues({ ...values, [f]: e.target.value })}
            />
          ))}
        </div>
        <Button disabled={!canLoc} onClick={loadLocalization} style={{ marginTop: 8 }}>
          Load
        </Button>
        <Button disabled={!canLoc} variant="contained" color="primary" onClick={saveLocalization} style={{ marginTop: 8, marginLeft: 8 }}>
          Save
        </Button>
      </Section>

      {notice && (
        <>
          <Divider />
          <p className={classes.message} style={{ color: notice.isError ? 'red' : 'inherit' }}>
            {notice.text}
          </p>
        </>
      )}
    </div>
  )
}

export default LibraryData
