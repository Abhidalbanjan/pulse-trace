import { NextResponse } from 'next/server';
import type { NextRequest } from 'next/server';

export function middleware(request: NextRequest) {
  // Public paths that don't require authentication
  const publicPaths = [
    '/login', 
    '/register', 
    '/api/v1/auth/login', 
    '/api/v1/auth/register',
    '/api/v1/auth/sso/login',
    '/api/v1/auth/sso/callback'
  ];
  const isPublicPath = publicPaths.some(path => request.nextUrl.pathname.startsWith(path));

  // Get the token from cookies
  const token = request.cookies.get('pulse_token')?.value;

  // If trying to access a protected route without a token, redirect to login
  if (!isPublicPath && !token) {
    return NextResponse.redirect(new URL('/login', request.url));
  }

  // If trying to access login page while already logged in, redirect to dashboard
  if (isPublicPath && token && request.nextUrl.pathname === '/login') {
    return NextResponse.redirect(new URL('/', request.url));
  }

  return NextResponse.next();
}

// Only run middleware on specific paths
export const config = {
  matcher: [
    /*
     * Match all request paths except for the ones starting with:
     * - _next/static (static files)
     * - _next/image (image optimization files)
     * - favicon.ico, icon.png, logo.png, logo.svg (public images)
     */
    '/((?!_next/static|_next/image|favicon.ico|icon.png|logo.png|logo.svg).*)',
  ],
};
