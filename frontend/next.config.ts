import type { NextConfig } from "next";

const nextConfig: NextConfig = {
  devIndicators: {
    appIsrStatus: false,
    buildActivity: false,
    buildActivityPosition: 'bottom-right',
  },
  async rewrites() {
    return [
      {
        source: '/api/v1/:path*',
        destination: 'http://127.0.0.1:8080/api/v1/:path*',
      },
      {
        source: '/api/traces/:path*',
        destination: 'http://127.0.0.1:8080/api/traces/:path*',
      },
      {
        source: '/api/traces',
        destination: 'http://127.0.0.1:8080/api/traces',
      },
      {
        source: '/api/services',
        destination: 'http://127.0.0.1:8080/api/services',
      }
    ];
  },
};

export default nextConfig;
