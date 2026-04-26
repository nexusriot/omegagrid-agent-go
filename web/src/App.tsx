import { BrowserRouter, Routes, Route, Navigate } from 'react-router-dom'
import Layout from './components/Layout'
import Chat from './pages/Chat'
import Memory from './pages/Memory'
import Skills from './pages/Skills'
import Scheduler from './pages/Scheduler'
import Health from './pages/Health'

export default function App() {
  return (
    <BrowserRouter basename="/ui">
      <Layout>
        <Routes>
          <Route path="/"          element={<Chat />}      />
          <Route path="/memory"    element={<Memory />}    />
          <Route path="/skills"    element={<Skills />}    />
          <Route path="/scheduler" element={<Scheduler />} />
          <Route path="/health"    element={<Health />}    />
          <Route path="*"          element={<Navigate to="/" replace />} />
        </Routes>
      </Layout>
    </BrowserRouter>
  )
}
