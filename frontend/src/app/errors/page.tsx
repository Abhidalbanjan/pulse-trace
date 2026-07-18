import { ErrorTrackingView } from "@/components/Errors/ErrorTrackingView";

export default function ErrorsPage() {
  return (
    <div style={{ padding: '24px', height: '100%', overflow: 'hidden' }}>
      <ErrorTrackingView />
    </div>
  );
}
