import { BrowserRouter, Navigate, NavLink, Route, Routes } from 'react-router-dom'
import { Cog, Download, LayoutDashboard, Settings, Tags, Waypoints } from 'lucide-react'
import OperationsPage from '@/pages/OperationsPage'
import IngestPage from '@/pages/IngestPage'
import EntitiesPage from '@/pages/EntitiesPage'
import RoutingPage from '@/pages/RoutingPage'
import SettingsPage from '@/pages/SettingsPage'

function NavItem({
  to,
  label,
  children,
}: {
  to: string
  label: string
  children: React.ReactNode
}) {
  return (
    <NavLink
      to={to}
      title={label}
      className={({ isActive }) =>
        [
          'flex h-9 w-9 items-center justify-center rounded-md transition-colors',
          'text-text-subtle hover:bg-panel-hover hover:text-foreground',
          isActive ? 'bg-panel-hover text-foreground' : '',
        ].join(' ')
      }
    >
      {children}
    </NavLink>
  )
}

function AppShell() {
  return (
    <div className="flex h-full w-full overflow-hidden bg-background text-foreground">
      {/* Nav rail */}
      <nav className="flex w-14 flex-col items-center gap-2 border-r border-border bg-panel py-4">
        {/* Logo */}
        <div
          className="mb-2 flex h-9 w-9 items-center justify-center rounded-md border border-border bg-panel-hover text-text-soft"
          title="Sysop"
        >
          <Cog className="h-5 w-5" />
        </div>

        <div className="mb-1 h-px w-8 bg-border" />

        <NavItem to="/operations" label="Operations">
          <LayoutDashboard className="h-4 w-4" />
        </NavItem>

        <NavItem to="/ingest" label="Ingest">
          <Download className="h-4 w-4" />
        </NavItem>

        <NavItem to="/entities" label="Entities">
          <Tags className="h-4 w-4" />
        </NavItem>

        <NavItem to="/routing" label="Routing">
          <Waypoints className="h-4 w-4" />
        </NavItem>

        {/* Settings pinned to the bottom */}
        <div className="mt-auto">
          <NavItem to="/settings" label="Settings">
            <Settings className="h-4 w-4" />
          </NavItem>
        </div>
      </nav>

      <div className="flex min-w-0 flex-1 flex-col bg-background">
        <main className="min-h-0 flex-1 overflow-auto bg-background">
          <Routes>
            <Route path="/" element={<Navigate to="/operations" replace />} />
            <Route path="/operations" element={<OperationsPage />} />
            <Route path="/ingest" element={<IngestPage />} />
            <Route path="/entities" element={<EntitiesPage />} />
            <Route path="/routing" element={<RoutingPage />} />
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
