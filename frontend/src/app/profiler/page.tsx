import { Suspense } from "react";
import { ContinuousProfilerView } from "@/components/Profiler/ContinuousProfilerView";

export default function ProfilerPage() {
  return (
    <div style={{ padding: '24px', height: '100%', overflow: 'hidden' }}>
      <Suspense fallback={null}>
        <ContinuousProfilerView />
      </Suspense>
    </div>
  );
}
