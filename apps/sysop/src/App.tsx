import { BrowserRouter, Navigate, Route, Routes, useLocation, useNavigate } from 'react-router-dom'
import {
  Activity,
  BookOpen,
  Cog,
  Download,
  Files,
  LayoutDashboard,
  Settings,
  Tags,
  Waypoints,
} from 'lucide-react'
import { NavRail, type NavRailItem } from '@hollis-labs/sysop-ui'
import OperationsPage from '@/pages/OperationsPage'
import LibraryPage from '@/pages/LibraryPage'
import IngestPage from '@/pages/IngestPage'
import EntitiesPage from '@/pages/EntitiesPage'
import RoutingPage from '@/pages/RoutingPage'
import ActivityPage from '@/pages/ActivityPage'
import SettingsPage from '@/pages/SettingsPage'
import ReaderPage from '@/pages/ReaderPage'
import ReaderDetailPage from '@/pages/ReaderDetailPage'

interface NavDest {
  path: string
  label: string
  icon: React.ReactNode
  footer?: boolean
}

const NAV_DESTS: NavDest[] = [
  { path: '/operations', label: 'Operations', icon: <LayoutDashboard className="h-4 w-4" /> },
  { path: '/reader', label: 'Reader', icon: <BookOpen className="h-4 w-4" /> },
  { path: '/library', label: 'Library', icon: <Files className="h-4 w-4" /> },
  { path: '/ingest', label: 'Ingest', icon: <Download className="h-4 w-4" /> },
  { path: '/entities', label: 'Entities', icon: <Tags className="h-4 w-4" /> },
  { path: '/routing', label: 'Routing', icon: <Waypoints className="h-4 w-4" /> },
  { path: '/activity', label: 'Activity', icon: <Activity className="h-4 w-4" /> },
  { path: '/settings', label: 'Settings', icon: <Settings className="h-4 w-4" />, footer: true },
]

export function AppShell() {
  const navigate = useNavigate()
  const location = useLocation()

  const navItems: NavRailItem[] = NAV_DESTS.map((dest) => ({
    key: dest.path,
    label: dest.label,
    icon: dest.icon,
    active: location.pathname.startsWith(dest.path),
    onSelect: () => navigate(dest.path),
    footer: dest.footer,
  }))

  return (
    <div className="flex h-full w-full overflow-hidden bg-background text-foreground">
      <NavRail items={navItems} logo={<Cog className="h-5 w-5" />} logoLabel="Sysop" />

      <div className="flex min-w-0 flex-1 flex-col bg-background">
        <main className="min-h-0 flex-1 overflow-auto bg-background">
          <Routes>
            <Route path="/" element={<Navigate to="/operations" replace />} />
            <Route path="/operations" element={<OperationsPage />} />
            <Route path="/reader" element={<ReaderPage />} />
            <Route path="/reader/:fragmentId" element={<ReaderDetailPage />} />
            <Route path="/library" element={<LibraryPage />} />
            <Route path="/ingest" element={<IngestPage />} />
            <Route path="/entities" element={<EntitiesPage />} />
            <Route path="/routing" element={<RoutingPage />} />
            <Route path="/activity" element={<ActivityPage />} />
            <Route path="/settings" element={<SettingsPage />} />
            <Route path="*" element={<Navigate to="/operations" replace />} />
          </Routes>
        </main>
      </div>
    </div>
  )
}

export default function App() {
  return (
    <BrowserRouter basename={import.meta.env.BASE_URL}>
      <AppShell />
    </BrowserRouter>
  )
}
