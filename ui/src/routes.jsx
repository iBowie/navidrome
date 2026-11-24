import React from 'react'
import { Route } from 'react-router-dom'
import Personal from './personal/Personal'
import LibraryData from './librarydata/LibraryData'

const routes = [
  <Route exact path="/personal" render={() => <Personal />} key={'personal'} />,
  <Route exact path="/librarydata" render={() => <LibraryData />} key={'librarydata'} />,
]

export default routes
