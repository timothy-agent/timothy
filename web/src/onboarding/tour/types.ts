// TourStep points at the element carrying data-tour="<target>".
export interface TourStep {
  target: string
  title: string
  body: string
  side?: 'top' | 'right' | 'bottom' | 'left'
}

// TourDef is one page's tour. Bumping version shows it again to
// operators who saw an older one.
export interface TourDef {
  page: string
  version: number
  steps: TourStep[]
}
