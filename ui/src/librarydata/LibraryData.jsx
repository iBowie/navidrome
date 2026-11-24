import React, { useCallback, useEffect, useState } from 'react'
import { Title } from 'react-admin'
import {
  Button,
  Card,
  CardContent,
  CardHeader,
  Divider,
  makeStyles,
  MenuItem,
  TextField,
} from '@material-ui/core'
import httpClient from '../dataProvider/httpClient'

const useStyles = makeStyles((theme) => ({
  root: { margin: '1em' },
  section: { marginBottom: theme.spacing(2) },
  fields: { display: 'flex', flexWrap: 'wrap', gap: theme.spacing(2), marginTop: theme.spacing(1) },
  field: { minWidth: 200, flex: 1 },
  message: { marginTop: theme.spacing(1) },
}))

const KINDS = [
  { value: 'songs', fields: ['title', 'artist', 'lyrics'] },
  { value: 'albums', fields: ['name', 'artist'] },
  { value: 'artists', fields: ['name'] },
]

const Section = ({ title, children }) => (
  <Card className={useStyles().section}>
    <CardHeader title={title} />
    <CardContent>{children}</CardContent>
  </Card>
)

const LibraryData = () => {
  const classes = useStyles()
  const [message, setMessage] = useState('')

  const notify = (text, isError) =>
    setMessage({ text, isError })

  const get = useCallback(async (url) => {
    const { json } = await httpClient(url)
    return json
  }, [])

  const put = useCallback(async (url, body) => {
    await httpClient(url, { method: 'PUT', body: JSON.stringify(body) })
  }, [])

  // Types
  const [typesText, setTypesText] = useState('')
  const loadTypes = useCallback(async () => {
    try {
      const t = await get('/api/librarydata/types')
      setTypesText(JSON.stringify(t, null, 2))
      notify('')
    } catch (e) {
      notify('Could not load types: ' + e.message, true)
    }
  }, [get])
  useEffect(() => void loadTypes(), [loadTypes])
  const saveTypes = async () => {
    try {
      await put('/api/librarydata/types', JSON.parse(typesText || '{}'))
      notify('Types saved')
    } catch (e) {
      notify('Could not save types: ' + e.message, true)
    }
  }

  // Links
  const [linkSongId, setLinkSongId] = useState('')
  const [linksText, setLinksText] = useState('')
  const loadLinks = async () => {
    try {
      const r = await get('/api/librarydata/links?ids=' + encodeURIComponent(linkSongId.trim()))
      setLinksText(JSON.stringify(r.links[linkSongId.trim()] || [], null, 2))
      notify('')
    } catch (e) {
      notify('Could not load links: ' + e.message, true)
    }
  }
  const saveLinks = async () => {
    try {
      await put('/api/librarydata/links', { id: linkSongId.trim(), links: JSON.parse(linksText || '[]') })
      notify('Links saved')
    } catch (e) {
      notify('Could not save links: ' + e.message, true)
    }
  }

  // Localization
  const [lang, setLang] = useState('')
  const [kind, setKind] = useState('songs')
  const [entityId, setEntityId] = useState('')
  const [values, setValues] = useState({})
  const loadLocalization = async () => {
    try {
      const r = await get(
        `/api/librarydata/localization?lang=${encodeURIComponent(lang.trim())}&kind=${kind}&id=${encodeURIComponent(entityId.trim())}`
      )
      setValues(r.values || {})
      notify('')
    } catch (e) {
      notify('Could not load localization: ' + e.message, true)
    }
  }
  const saveLocalization = async () => {
    try {
      await put('/api/librarydata/localization', {
        lang: lang.trim(),
        kind,
        id: entityId.trim(),
        values: Object.fromEntries(Object.entries(values).filter(([, v]) => v !== '')),
      })
      notify('Localization saved')
    } catch (e) {
      notify('Could not save localization: ' + e.message, true)
    }
  }

  const kindFields = KINDS.find((k) => k.value === kind).fields
  const canEdit = lang.trim() !== '' && entityId.trim() !== ''

  return (
    <div className={classes.root}>
      <Title title="Navidrome - Library data" />
      <Section title="Playlist and album types">
        <TextField
          multiline
          rows={8}
          fullWidth
          variant="outlined"
          value={typesText}
          onChange={(e) => setTypesText(e.target.value)}
          helperText='JSON: {"playlists": {"<id>": "soundtrack"}, "albums": {"<id>": "single"}}'
        />
        <Button variant="contained" color="primary" onClick={saveTypes} style={{ marginTop: 8 }}>
          Save
        </Button>
        <Button onClick={loadTypes} style={{ marginTop: 8, marginLeft: 8 }}>
          Reload
        </Button>
      </Section>
      <Section title="Song links">
        <div className={classes.fields}>
          <TextField
            className={classes.field}
            label="Song id"
            value={linkSongId}
            onChange={(e) => setLinkSongId(e.target.value)}
          />
        </div>
        {linksText !== '' && (
          <TextField
            multiline
            rows={8}
            fullWidth
            variant="outlined"
            style={{ marginTop: 8 }}
            value={linksText}
            onChange={(e) => setLinksText(e.target.value)}
            helperText='JSON: [{"a": "<song id>", "b": "<song id>", "kind": "version|alternate|counterpart", "context": ["<playlist/album id>"], "merge": true}]'
          />
        )}
        <Button disabled={!linkSongId.trim()} onClick={loadLinks} style={{ marginTop: 8 }}>
          Load
        </Button>
        <Button
          disabled={!linkSongId.trim()}
          variant="contained"
          color="primary"
          onClick={saveLinks}
          style={{ marginTop: 8, marginLeft: 8 }}
        >
          Save
        </Button>
      </Section>
      <Section title="Localized tags">
        <div className={classes.fields}>
          <TextField className={classes.field} label="Language (e.g. ja)" value={lang} onChange={(e) => setLang(e.target.value)} />
          <TextField
            className={classes.field}
            select
            label="What"
            value={kind}
            onChange={(e) => {
              setKind(e.target.value)
              setValues({})
            }}
          >
            {KINDS.map((k) => (
              <MenuItem key={k.value} value={k.value}>
                {k.value}
              </MenuItem>
            ))}
          </TextField>
          <TextField className={classes.field} label="Id" value={entityId} onChange={(e) => setEntityId(e.target.value)} />
          {kindFields.map((f) => (
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
        <Button disabled={!canEdit} onClick={loadLocalization} style={{ marginTop: 8 }}>
          Load
        </Button>
        <Button
          disabled={!canEdit}
          variant="contained"
          color="primary"
          onClick={saveLocalization}
          style={{ marginTop: 8, marginLeft: 8 }}
        >
          Save
        </Button>
      </Section>
      {message && (
        <>
          <Divider />
          <p className={classes.message} style={{ color: message.isError ? 'red' : 'inherit' }}>
            {message.text}
          </p>
        </>
      )}
    </div>
  )
}

export default LibraryData
