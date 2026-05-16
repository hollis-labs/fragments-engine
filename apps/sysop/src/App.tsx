import { BrowserRouter, Navigate, NavLink, Route, Routes } from 'react-router-dom'
import { Cog, Inbox } from 'lucide-react'
import InboxPage from '@/pages/InboxPage'

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
      end={to === '/inbox'}
      title={label}
      className={({ isActive }) =>
        [
          'flex h-9 w-9 items-center justify-center rounded-md transition-colors',
          'text-zinc-500 hover:bg-zinc-900 hover:text-zinc-100',
          isActive ? 'bg-zinc-900 text-zinc-100' : '',
        ].join(' ')
      }
    >
      {children}
    </NavLink>
  )
}

function AppShell() {
  return (
    <div className="flex h-screen w-screen overflow-hidden bg-zinc-950 text-zinc-100">
      {/* Nav rail */}
      <nav className="flex w-14 flex-col items-center gap-2 border-r border-zinc-800 bg-zinc-950 py-4">
        {/* Logo */}
        <div
          className="mb-2 flex h-9 w-9 items-center justify-center rounded-md border border-zinc-800 bg-zinc-900 text-zinc-300"
          title="Sysop"
        >
          <Cog className="h-5 w-5" />
        </div>

        <div className="h-px w-8 bg-zinc-800 mb-1" />

        <NavItem to="/inbox" label="Inbox">
          <Inbox className="h-4 w-4" />
        </NavItem>
      </nav>

      <main className="flex-1 overflow-auto bg-zinc-950">
        <Routes>
          <Route path="/" element={<Navigate to="/inbox" replace />} />
          <Route path="/inbox" element={<InboxPage />} />
          <Route path="*" element={<Navigate to="/inbox" replace />} />
        </Routes>
      </main>
    </div>
  )
}

export default function App() {
  return (
    <BrowserRouter>
      <AppShell />
    </BrowserRouter>
  )
}
