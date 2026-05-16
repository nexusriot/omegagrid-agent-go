import { NavLink } from 'react-router-dom'
import {
  MessageSquare, Database, Wrench, Clock, Activity, Omega, History,
} from 'lucide-react'
import type { ReactNode } from 'react'

const NAV = [
  { to: '/',          icon: MessageSquare, label: 'Chat'      },
  { to: '/memory',    icon: Database,      label: 'Memory'    },
  { to: '/skills',    icon: Wrench,        label: 'Skills'    },
  { to: '/scheduler', icon: Clock,         label: 'Scheduler' },
  { to: '/activity',  icon: History,       label: 'Activity'  },
  { to: '/health',    icon: Activity,      label: 'Health'    },
]

export default function Layout({ children }: { children: ReactNode }) {
  return (
    <div className="flex h-screen w-full overflow-hidden bg-surface text-gray-100 font-sans">
      {/*Left nav rail*/}
      <aside className="flex w-14 flex-col items-center gap-1 border-r border-surface-border bg-surface-raised py-4 shrink-0">
        {/* Logo */}
        <div className="mb-4 flex h-9 w-9 items-center justify-center rounded-xl bg-accent/20 text-accent">
          <Omega size={20} strokeWidth={2.5} />
        </div>

        {NAV.map(({ to, icon: Icon, label }) => (
          <NavLink
            key={to}
            to={to}
            end={to === '/'}
            title={label}
            className={({ isActive }) =>
              [
                'flex h-10 w-10 items-center justify-center rounded-xl transition-colors duration-150',
                isActive
                  ? 'bg-accent/20 text-accent'
                  : 'text-gray-500 hover:bg-surface-overlay hover:text-gray-300',
              ].join(' ')
            }
          >
            <Icon size={18} />
          </NavLink>
        ))}
      </aside>

      {/*Main content*/}
      <main className="flex flex-1 flex-col overflow-hidden">
        {children}
      </main>
    </div>
  )
}
