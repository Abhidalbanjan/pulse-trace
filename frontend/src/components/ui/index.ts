// Design-system primitives (ROAD_TO_100 · F0.4). Small, theme-aware building
// blocks so screens stop re-implementing loading/error/empty states, blocking
// alert()/confirm() dialogs, and one-off notifications inline.
export { StateBoundary } from './StateBoundary';
export type { StateBoundaryProps } from './StateBoundary';
export { ToastProvider, useToast } from './Toast';
export { ConfirmDialog } from './ConfirmDialog';
export type { ConfirmDialogProps } from './ConfirmDialog';
