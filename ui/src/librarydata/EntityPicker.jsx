import React, { useEffect, useMemo, useState } from 'react'
import { useGetList } from 'react-admin'
import { KIND_NAME_FIELD } from './fields'
import { List, ListItem, ListItemText, Paper, TextField } from '@material-ui/core'

const KIND_SORT = {
  song: { field: 'title', order: 'ASC' },
  album: { field: 'name', order: 'ASC' },
  artist: { field: 'name', order: 'ASC' },
  playlist: { field: 'name', order: 'ASC' },
}

/** A search-as-you-type picker over a react-admin resource; onPick receives the entity (or null). */
export const EntityPicker = ({ resource, label, value, onPick, excludeId }) => {
  const [search, setSearch] = useState(
    value ? value[KIND_NAME_FIELD[resource]] ?? '' : '',
  )
  const [open, setOpen] = useState(false)
  const { ids, data } = useGetList(resource, {
    pagination: { page: 1, perPage: 500 },
    sort: KIND_SORT[resource],
  })
  useEffect(() => {
    setSearch(value ? value[KIND_NAME_FIELD[resource]] ?? '' : '')
  }, [value, resource])
  const matches = useMemo(() => {
    const q = search.trim().toLowerCase()
    return (ids || [])
      .map((id) => data[id])
      .filter((e) => e && e.id !== excludeId)
      .filter(
        (e) =>
          !q || (e[KIND_NAME_FIELD[resource]] || '').toLowerCase().includes(q),
      )
      .slice(0, 8)
  }, [ids, data, search, resource, excludeId])
  return (
    <div style={{ position: 'relative', flex: 1, minWidth: 220 }}>
      <TextField
        fullWidth
        label={label}
        value={search}
        onFocus={() => setOpen(true)}
        onBlur={() => setTimeout(() => setOpen(false), 150)}
        onChange={(e) => {
          setSearch(e.target.value)
          setOpen(true)
          onPick(null)
        }}
      />
      {open && matches.length > 0 && (
        <Paper
          style={{
            position: 'absolute',
            zIndex: 10,
            width: '100%',
            maxHeight: 260,
            overflow: 'auto',
          }}
        >
          <List dense>
            {matches.map((e) => (
              <ListItem
                button
                key={e.id}
                onMouseDown={(ev) => {
                  ev.preventDefault()
                  onPick(e)
                  setOpen(false)
                }}
              >
                <ListItemText
                  primary={e[KIND_NAME_FIELD[resource]] || e.id}
                  secondary={e.artist || e.album || e.id}
                />
              </ListItem>
            ))}
          </List>
        </Paper>
      )}
    </div>
  )
}
