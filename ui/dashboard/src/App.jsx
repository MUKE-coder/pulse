import { Routes, Route, Navigate } from 'react-router-dom'
import { useAuth } from './context/AuthContext'
import Layout from './components/Layout'
import Login from './pages/Login'
import Dashboard from './pages/Dashboard'
import RoutesPage from './pages/Routes'
import DatabasePage from './pages/Database'
import ErrorsPage from './pages/Errors'
import RuntimePage from './pages/Runtime'
import HealthPage from './pages/Health'
import AlertsPage from './pages/Alerts'
import SettingsPage from './pages/Settings'

function ProtectedRoute({ children }) {
  const { isAuthenticated } = useAuth()
  if (!isAuthenticated) return <Navigate to="/pulse/ui/login" replace />
  return children
}

export default function App() {
  return (
    <Routes>
      <Route path="/pulse/ui/login" element={<Login />} />
      <Route path="/pulse/ui" element={<ProtectedRoute><Layout /></ProtectedRoute>}>
        <Route index element={<Dashboard />} />
        <Route path="routes" element={<RoutesPage />} />
        <Route path="database" element={<DatabasePage />} />
        <Route path="errors" element={<ErrorsPage />} />
        <Route path="runtime" element={<RuntimePage />} />
        <Route path="health" element={<HealthPage />} />
        <Route path="alerts" element={<AlertsPage />} />
        <Route path="settings" element={<SettingsPage />} />
      </Route>
      <Route path="*" element={<Navigate to="/pulse/ui" replace />} />
    </Routes>
  )
}
