import { ServiceDetailView } from "@/components/Services/ServiceDetailView";

export default async function ServiceDetailPage({
  params,
}: {
  params: Promise<{ name: string }>;
}) {
  const { name } = await params;
  return (
    <div style={{ padding: '24px', height: '100%', overflow: 'hidden' }}>
      <ServiceDetailView serviceName={decodeURIComponent(name)} />
    </div>
  );
}
