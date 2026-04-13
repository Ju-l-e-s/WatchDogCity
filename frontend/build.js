const fs = require('fs');
const crypto = require('crypto');
const path = require('path');

function getHash(filePath) {
    const fileBuffer = fs.readFileSync(filePath);
    const hashSum = crypto.createHash('md5');
    hashSum.update(fileBuffer);
    return hashSum.digest('hex').substring(0, 8);
}

function updateFileWithHash(fileName, ext) {
    const filePath = path.join(__dirname, `${fileName}.${ext}`);
    if (!fs.existsSync(filePath)) {
        console.error(`File not found: ${filePath}`);
        return null;
    }
    const hash = getHash(filePath);
    const newFileName = `${fileName}.${hash}.${ext}`;
    const newFilePath = path.join(__dirname, newFileName);
    
    fs.renameSync(filePath, newFilePath);
    console.log(`Renamed ${fileName}.${ext} to ${newFileName}`);
    return newFileName;
}

const jsFile = updateFileWithHash('app', 'js');
const cssFile = updateFileWithHash('style', 'css');

if (jsFile || cssFile) {
    const indexHtmlPath = path.join(__dirname, 'index.html');
    let indexHtml = fs.readFileSync(indexHtmlPath, 'utf8');

    if (jsFile) {
        indexHtml = indexHtml.replace(/src="app\.js"/g, `src="${jsFile}"`);
    }
    if (cssFile) {
        indexHtml = indexHtml.replace(/href="style\.css"/g, `href="${cssFile}"`);
    }

    fs.writeFileSync(indexHtmlPath, indexHtml);
    console.log('Updated index.html with hashed filenames.');
}