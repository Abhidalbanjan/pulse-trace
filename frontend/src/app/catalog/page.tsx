import { ServiceCatalog } from '@/components/Catalog/ServiceCatalog';

export default function CatalogPage() {
  return (
    <div style={{ display: 'flex', flexDirection: 'column', height: '100%' }}>
      <ServiceCatalog />
    </div>
  );
}
