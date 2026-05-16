export default function InboxPage() {
  return (
    <section className="flex min-h-screen items-center justify-center px-6 py-16">
      <div className="max-w-xl rounded-2xl border border-zinc-800 bg-zinc-900/70 p-8 text-zinc-100 shadow-2xl shadow-black/20">
        <p className="text-xs font-semibold uppercase tracking-[0.32em] text-zinc-500">
          Sysop
        </p>
        <h1 className="mt-3 text-3xl font-semibold tracking-tight">
          Inbox is the Phase 1 surface.
        </h1>
        <p className="mt-4 text-sm leading-6 text-zinc-400">
          This vendored shell keeps the upstream GUI foundation, but only mounts the inbox
          route while Fragments Engine defines the Phase 1 workflow.
        </p>
      </div>
    </section>
  )
}
