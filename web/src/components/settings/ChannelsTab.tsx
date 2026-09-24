import { Route, Routes } from 'react-router'
import { ChannelAdd } from './ChannelAdd'
import { ChannelEdit } from './ChannelEdit'
import { ChannelsList } from './ChannelsList'

// ChannelsTab is the route container for the channels area: list, add
// page and manage page, same split as DestinationsTab.
export function ChannelsTab() {
  return (
    <Routes>
      <Route path="/" element={<ChannelsList />} />
      <Route path="new" element={<ChannelAdd />} />
      <Route path=":id" element={<ChannelEdit />} />
    </Routes>
  )
}
