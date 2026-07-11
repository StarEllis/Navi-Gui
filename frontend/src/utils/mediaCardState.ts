export const areMediaCardMediaPropsEqual = (previous: any, next: any) => previous === next;

export const shouldOpenMediaFromCardKey = (key: string, targetIsCard: boolean) => (
    targetIsCard && (key === 'Enter' || key === ' ')
);
