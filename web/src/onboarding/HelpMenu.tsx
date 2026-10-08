import { CircleHelp } from 'lucide-react'
import { Fragment } from 'react'
import { IconButton } from '../components/timothy/icon-button'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '../components/ui/dropdown-menu'
import { useHelpActions } from './helpActions'

export function HelpMenu() {
  const actions = useHelpActions()
  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <IconButton label="Help" icon={CircleHelp} size="sm" tooltip={false} />
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end">
        {actions.map((a) =>
          a.href ? (
            <Fragment key={a.id}>
              <DropdownMenuSeparator />
              <DropdownMenuItem asChild>
                <a href={a.href} target="_blank" rel="noreferrer">
                  <a.icon />
                  {a.label}
                </a>
              </DropdownMenuItem>
            </Fragment>
          ) : (
            <DropdownMenuItem key={a.id} disabled={a.disabled} onSelect={a.run}>
              <a.icon />
              {a.label}
            </DropdownMenuItem>
          ),
        )}
      </DropdownMenuContent>
    </DropdownMenu>
  )
}
