# Frontend platform (ROAD_TO_100 · F0.4)

The shared building blocks that let screens stop re-implementing data-fetching,
state rendering, and dialogs inline. Use these instead of `fetchWithAuth` string
literals, `useState<any[]>`, `alert()`, and `confirm()`.

## Data: typed client + resource hook

```tsx
import { api } from '@/lib/api/client';
import type { Role } from '@/lib/api/types';
import { useApiResource } from '@/lib/hooks/useApiResource';
import { StateBoundary } from '@/components/ui';

const roles = useApiResource<Role[]>(
  () => api.getData<Role[]>('/api/v1/admin/roles').then((d) => d ?? []),
  { pollMs: 5000 }, // optional live polling
);

<StateBoundary loading={roles.loading} error={roles.error}
  empty={(roles.data ?? []).length === 0} onRetry={roles.refetch}>
  {/* render roles.data */}
</StateBoundary>
```

- `api.get/post/put/patch/del<T>` — typed transport; throws `ApiError` (status +
  server message) on non-2xx.
- `api.getData<T>` / `api.list<T>` — unwrap the standard `{ success, data, meta }`
  envelope.
- `useApiResource` — `{ data, error, loading, refetch, setData }`, optional
  `pollMs` (silent polling) and `key` (re-fetch when a param changes).

## Feedback: toast (replaces `alert()`)

```tsx
import { useToast } from '@/components/ui';
const toast = useToast();
toast.success('Saved'); toast.error('Failed: …'); toast.info('…');
```

`<ToastProvider>` is mounted once in the root layout.

## Destructive actions: ConfirmDialog (replaces `confirm()`)

```tsx
const [pending, setPending] = useState<string | null>(null);
const [busy, setBusy] = useState(false);

<ConfirmDialog open={pending !== null} danger busy={busy}
  title="Delete X?" confirmLabel="Delete"
  onConfirm={doDelete} onCancel={() => setPending(null)} />
```

Focus-managed, Escape-to-cancel, `role="dialog"`/`aria-modal`, backdrop dismiss.

## Accessibility

`tests/e2e/a11y.spec.ts` runs axe-core against key screens and fails on **critical**
WCAG 2.1 A/AA violations. New screens should keep that bar green (label inputs,
give icon-only buttons an `aria-label`, use real headings).

See `src/components/Settings/RolesPanel.tsx` for a screen using all of the above.
