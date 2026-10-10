// Every <Route path> in App.tsx, with the sidebar label for pages that
// have one. Dependency-free so scripts/export-routes.ts can import it
// under plain node; routes.test.ts keeps it in step with App.tsx.
export const appRoutes: { path: string; label?: string }[] = [
  { path: '/', label: 'Home' },
  { path: '/chat/:id?', label: 'Chat' },
  { path: '/research/:id?' },
  { path: '/sessions/:id' },
  { path: '/analytics', label: 'Analytics' },
  { path: '/dashboard' },
  { path: '/missions', label: 'Missions' },
  { path: '/missions/new' },
  { path: '/missions/schedules/:id/edit' },
  { path: '/missions/:id' },
  { path: '/automations', label: 'Automations' },
  { path: '/automations/new' },
  { path: '/automations/:id/edit' },
  { path: '/automations/:id' },
  { path: '/knowledge/*', label: 'Knowledge' },
  { path: '/memory/knowledge/*' },
  { path: '/memory/*', label: 'Memory' },
  { path: '/settings/*', label: 'Settings' },
  { path: '/settings' },
  { path: '/design' },
  // Rendered outside <Routes> (App.tsx matches pathname directly).
  { path: '/welcome', label: 'Welcome' },
]
