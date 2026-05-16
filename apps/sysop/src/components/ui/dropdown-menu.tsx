import * as BaseMenu from '@base-ui/react/menu'
import { Check } from 'lucide-react'
import { cn } from '@/lib/utils'

function DropdownMenu(props: React.ComponentProps<typeof BaseMenu.Menu.Root>) {
  return <BaseMenu.Menu.Root modal={false} {...props} />
}

function DropdownMenuTrigger(props: React.ComponentProps<typeof BaseMenu.Menu.Trigger>) {
  return <BaseMenu.Menu.Trigger data-slot="dropdown-menu-trigger" {...props} />
}

function DropdownMenuContent({
  className,
  sideOffset = 10,
  ...props
}: React.ComponentProps<typeof BaseMenu.Menu.Positioner> & React.ComponentProps<typeof BaseMenu.Menu.Popup>) {
  return (
    <BaseMenu.Menu.Portal>
      <BaseMenu.Menu.Positioner align="end" sideOffset={sideOffset} {...props}>
        <BaseMenu.Menu.Popup
          data-slot="dropdown-menu-content"
          className={cn(
            'z-50 min-w-64 rounded-xl border border-border bg-panel-overlay p-1.5 text-sm text-popover-foreground shadow-2xl shadow-shadow-strong outline-none backdrop-blur-xl',
            className,
          )}
        />
      </BaseMenu.Menu.Positioner>
    </BaseMenu.Menu.Portal>
  )
}

function DropdownMenuLabel({ className, ...props }: React.ComponentProps<'div'>) {
  return (
    <div
      data-slot="dropdown-menu-label"
      className={cn('px-3 pb-1 pt-2 text-[11px] font-semibold uppercase tracking-[0.28em] text-text-subtle', className)}
      {...props}
    />
  )
}

function DropdownMenuSeparator({ className, ...props }: React.ComponentProps<typeof BaseMenu.Menu.Separator>) {
  return <BaseMenu.Menu.Separator data-slot="dropdown-menu-separator" className={cn('my-1 h-px bg-border', className)} {...props} />
}

function DropdownMenuRadioGroup(props: React.ComponentProps<typeof BaseMenu.Menu.RadioGroup>) {
  return <BaseMenu.Menu.RadioGroup data-slot="dropdown-menu-radio-group" {...props} />
}

function DropdownMenuRadioItem({
  className,
  children,
  ...props
}: React.ComponentProps<typeof BaseMenu.Menu.RadioItem>) {
  return (
    <BaseMenu.Menu.RadioItem
      data-slot="dropdown-menu-radio-item"
      closeOnClick
      className={cn(
        'grid cursor-default grid-cols-[1rem_1fr] items-start gap-3 rounded-lg px-3 py-2 text-sm text-text-soft outline-none transition-colors',
        'focus:bg-panel-hover focus:text-foreground data-[highlighted]:bg-panel-hover data-[highlighted]:text-foreground',
        'data-[checked=true]:text-foreground',
        className,
      )}
      {...props}
    >
      <BaseMenu.Menu.RadioItemIndicator
        keepMounted
        className="mt-0.5 flex h-4 w-4 items-center justify-center text-accent opacity-0 transition-opacity data-[checked=true]:opacity-100"
      >
        <Check className="h-3.5 w-3.5" />
      </BaseMenu.Menu.RadioItemIndicator>
      {children}
    </BaseMenu.Menu.RadioItem>
  )
}

export {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuLabel,
  DropdownMenuRadioGroup,
  DropdownMenuRadioItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
}
