import { ContinuousProfilerView } from "@/components/Profiler/ContinuousProfilerView";

export default function ProfilerPage() {
  return (
    <div style={{ padding: '24px', height: '100%', overflow: 'hidden' }}>
      <ContinuousProfilerView />
    </div>
  );
}
