import { Suspense } from 'react';
import { QueryWorkbench } from '@/components/Query/QueryWorkbench';

export default function QueryPage() {
  return (
    <div style={{ display: 'flex', flexDirection: 'column', height: '100%' }}>
      {/* useSearchParams reads the shared `?sql=` link, so the tree needs a
          boundary; nothing useful renders before the editor, hence null. */}
      <Suspense fallback={null}>
        <QueryWorkbench />
      </Suspense>
    </div>
  );
}
