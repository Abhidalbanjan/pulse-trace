import { Suspense } from "react";
import { TracesView } from "@/components/Traces/TracesView";

export default function TracesPage() {
  return (
    <div style={{ padding: '24px', height: '100%', overflow: 'hidden' }}>
      <Suspense fallback={null}>
        <TracesView />
      </Suspense>
    </div>
  );
}
