const { generateMatrix } = require('./generate-matrix');

test('generateMatrix creates correct steps', () => {
  const platforms = [
    { os: 'linux', arch: 'amd64', image: 'golang:1.26-alpine' }
  ];
  
  const steps = generateMatrix(platforms);
  
  expect(steps).toHaveLength(1);
  expect(steps[0].id).toBe('build-linux-amd64');
  expect(steps[0].env.GOOS).toBe('linux');
});

test('generateMatrix handles multiple platforms', () => {
  const platforms = [
    { os: 'linux', arch: 'amd64', image: 'golang:1.26-alpine' },
    { os: 'darwin', arch: 'arm64', image: 'golang:1.26-alpine' }
  ];
  
  const steps = generateMatrix(platforms);
  
  expect(steps).toHaveLength(2);
  expect(steps[1].id).toBe('build-darwin-arm64');
});
